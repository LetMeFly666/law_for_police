from __future__ import annotations

import hashlib
import json
import os
import secrets
import sqlite3
import time
import uuid
from pathlib import Path
from typing import Any
from contextlib import contextmanager

from fastapi import FastAPI, Header, HTTPException, Request
from fastapi.responses import FileResponse, JSONResponse
from pydantic import BaseModel, Field

BASE_DIR = Path(__file__).resolve().parent.parent
WEB_DIR = BASE_DIR / "web"
DATA_DIR = Path(os.getenv("DATA_DIR", str(BASE_DIR / "data"))).resolve()
DB_PATH = DATA_DIR / "law_study.sqlite3"
ADMIN_TOKEN = os.getenv("ADMIN_TOKEN", "").strip()
MAX_SYNC_BYTES = 2 * 1024 * 1024
PAIR_TTL_SECONDS = 10 * 60
CODE_ALPHABET = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"  # excludes ambiguous 0/O/1/I

app = FastAPI(title="法律基础知识学习本 API", version="1.0.0", docs_url=None, redoc_url=None)


@contextmanager
def connect():
    DATA_DIR.mkdir(parents=True, exist_ok=True)
    con = sqlite3.connect(DB_PATH, timeout=15)
    con.row_factory = sqlite3.Row
    con.execute("PRAGMA foreign_keys = ON")
    con.execute("PRAGMA busy_timeout = 15000")
    try:
        yield con
        con.commit()
    except Exception:
        con.rollback()
        raise
    finally:
        con.close()


def init_db() -> None:
    with connect() as con:
        con.execute("PRAGMA journal_mode = WAL")
        con.executescript(
            """
            CREATE TABLE IF NOT EXISTS groups (
                id TEXT PRIMARY KEY,
                created_at INTEGER NOT NULL
            );
            CREATE TABLE IF NOT EXISTS devices (
                id TEXT PRIMARY KEY,
                token_hash TEXT UNIQUE NOT NULL,
                group_id TEXT NOT NULL REFERENCES groups(id),
                created_at INTEGER NOT NULL,
                last_seen INTEGER NOT NULL
            );
            CREATE INDEX IF NOT EXISTS idx_devices_group ON devices(group_id);
            CREATE TABLE IF NOT EXISTS pair_codes (
                code_hash TEXT PRIMARY KEY,
                group_id TEXT NOT NULL REFERENCES groups(id),
                created_at INTEGER NOT NULL,
                expires_at INTEGER NOT NULL,
                used_at INTEGER
            );
            CREATE INDEX IF NOT EXISTS idx_pair_codes_expiry ON pair_codes(expires_at);
            CREATE TABLE IF NOT EXISTS sync_events (
                seq INTEGER PRIMARY KEY AUTOINCREMENT,
                group_id TEXT NOT NULL REFERENCES groups(id),
                item_key TEXT NOT NULL,
                value_json TEXT NOT NULL,
                updated_at INTEGER NOT NULL,
                created_at INTEGER NOT NULL
            );
            CREATE INDEX IF NOT EXISTS idx_sync_events_group_seq ON sync_events(group_id, seq);
            CREATE TABLE IF NOT EXISTS sync_items (
                group_id TEXT NOT NULL REFERENCES groups(id),
                item_key TEXT NOT NULL,
                value_json TEXT NOT NULL,
                updated_at INTEGER NOT NULL,
                seq INTEGER NOT NULL,
                PRIMARY KEY(group_id, item_key)
            );
            CREATE TABLE IF NOT EXISTS experiences (
                id TEXT PRIMARY KEY,
                group_id TEXT NOT NULL REFERENCES groups(id),
                question_id TEXT NOT NULL,
                nickname TEXT NOT NULL,
                content TEXT NOT NULL,
                created_at INTEGER NOT NULL
            );
            CREATE INDEX IF NOT EXISTS idx_experiences_question ON experiences(question_id, created_at DESC);
            CREATE INDEX IF NOT EXISTS idx_experiences_group ON experiences(group_id, created_at DESC);
            CREATE TABLE IF NOT EXISTS feedback (
                id TEXT PRIMARY KEY,
                group_id TEXT NOT NULL REFERENCES groups(id),
                nickname TEXT NOT NULL,
                email TEXT NOT NULL DEFAULT '',
                category TEXT NOT NULL,
                content TEXT NOT NULL,
                status TEXT NOT NULL DEFAULT 'new',
                created_at INTEGER NOT NULL
            );
            CREATE INDEX IF NOT EXISTS idx_feedback_status ON feedback(status, created_at DESC);
            CREATE TABLE IF NOT EXISTS rate_hits (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                ip TEXT NOT NULL,
                scope TEXT NOT NULL,
                created_at INTEGER NOT NULL
            );
            CREATE INDEX IF NOT EXISTS idx_rate_hits_lookup ON rate_hits(ip, scope, created_at);
            """
        )


@app.on_event("startup")
def startup() -> None:
    init_db()


@app.middleware("http")
async def security_headers(request: Request, call_next):
    response = await call_next(request)
    response.headers["X-Content-Type-Options"] = "nosniff"
    response.headers["Referrer-Policy"] = "strict-origin-when-cross-origin"
    response.headers["X-Frame-Options"] = "DENY"
    response.headers["Permissions-Policy"] = "camera=(), microphone=(), geolocation=()"
    response.headers["Content-Security-Policy"] = (
        "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; "
        "img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'"
    )
    return response


def token_hash(token: str) -> str:
    return hashlib.sha256(token.encode("utf-8")).hexdigest()


def code_hash(code: str) -> str:
    return hashlib.sha256(code.replace("-", "").upper().encode("utf-8")).hexdigest()


def get_device(authorization: str | None) -> sqlite3.Row:
    if not authorization or not authorization.startswith("Bearer "):
        raise HTTPException(status_code=401, detail="设备身份尚未建立，请刷新页面重试")
    token = authorization[7:].strip()
    if len(token) < 30 or len(token) > 200:
        raise HTTPException(status_code=401, detail="设备身份无效")
    with connect() as con:
        row = con.execute(
            "SELECT id, token_hash, group_id FROM devices WHERE token_hash = ?",
            (token_hash(token),),
        ).fetchone()
        if row:
            con.execute("UPDATE devices SET last_seen = ? WHERE id = ?", (int(time.time()), row["id"]))
    if not row:
        raise HTTPException(status_code=401, detail="设备身份已失效，请重新连接设备")
    return row


def client_ip(request: Request) -> str:
    # Keep uvicorn behind a trusted local reverse proxy and configure proxy-headers there.
    return request.client.host if request.client else "unknown"


def enforce_rate(request: Request, scope: str, limit: int, window_seconds: int) -> None:
    now = int(time.time())
    ip = client_ip(request)
    with connect() as con:
        con.execute("BEGIN IMMEDIATE")
        con.execute("DELETE FROM rate_hits WHERE created_at < ?", (now - window_seconds,))
        count = con.execute(
            "SELECT COUNT(*) FROM rate_hits WHERE ip = ? AND scope = ? AND created_at >= ?",
            (ip, scope, now - window_seconds),
        ).fetchone()[0]
        if count >= limit:
            raise HTTPException(status_code=429, detail="操作过于频繁，请稍后再试")
        con.execute("INSERT INTO rate_hits(ip, scope, created_at) VALUES(?,?,?)", (ip, scope, now))


class ExchangeChange(BaseModel):
    key: str = Field(min_length=1, max_length=180)
    value: Any
    updated_at: int = Field(ge=0)


class ExchangeRequest(BaseModel):
    cursor: int = Field(default=0, ge=0)
    changes: list[ExchangeChange] = Field(default_factory=list, max_length=500)


class JoinRequest(BaseModel):
    code: str = Field(min_length=6, max_length=20)


class ExperienceRequest(BaseModel):
    question_id: str = Field(min_length=3, max_length=40)
    nickname: str = Field(min_length=1, max_length=30)
    content: str = Field(min_length=2, max_length=2000)


class FeedbackRequest(BaseModel):
    nickname: str = Field(min_length=1, max_length=30)
    email: str = Field(default="", max_length=254)
    category: str = Field(min_length=1, max_length=30)
    content: str = Field(min_length=5, max_length=3000)


class FeedbackStatusRequest(BaseModel):
    status: str = Field(pattern="^(new|reviewing|resolved|rejected)$")


@app.get("/api/health")
def health():
    return {"ok": True, "service": "law-study", "time": int(time.time())}


@app.post("/api/anonymous")
def create_anonymous_account(request: Request):
    """Create an anonymous account/device. The random token is kept only on the client."""
    enforce_rate(request, "anonymous", 100, 3600)
    now = int(time.time())
    group_id = str(uuid.uuid4())
    device_id = str(uuid.uuid4())
    token = secrets.token_urlsafe(40)
    with connect() as con:
        con.execute("INSERT INTO groups(id, created_at) VALUES(?,?)", (group_id, now))
        con.execute(
            "INSERT INTO devices(id, token_hash, group_id, created_at, last_seen) VALUES(?,?,?,?,?)",
            (device_id, token_hash(token), group_id, now, now),
        )
    return {"token": token, "device_id": device_id, "group_id": group_id}


@app.post("/api/sync/pair-code")
def create_pair_code(request: Request, authorization: str | None = Header(default=None)):
    enforce_rate(request, "pair-code", 10, 3600)
    device = get_device(authorization)
    now = int(time.time())
    code = "".join(secrets.choice(CODE_ALPHABET) for _ in range(8))
    with connect() as con:
        con.execute("DELETE FROM pair_codes WHERE expires_at < ? OR used_at IS NOT NULL", (now,))
        con.execute(
            "INSERT INTO pair_codes(code_hash, group_id, created_at, expires_at) VALUES(?,?,?,?)",
            (code_hash(code), device["group_id"], now, now + PAIR_TTL_SECONDS),
        )
    return {"code": f"{code[:4]}-{code[4:]}", "expires_in": PAIR_TTL_SECONDS}


@app.post("/api/sync/join")
def join_group(payload: JoinRequest, request: Request, authorization: str | None = Header(default=None)):
    enforce_rate(request, "pair-join", 8, 600)
    device = get_device(authorization)
    now = int(time.time())
    digest = code_hash(payload.code)
    with connect() as con:
        con.execute("BEGIN IMMEDIATE")
        row = con.execute(
            "SELECT code_hash, group_id, expires_at, used_at FROM pair_codes WHERE code_hash = ?",
            (digest,),
        ).fetchone()
        if not row or row["used_at"] is not None or row["expires_at"] < now:
            raise HTTPException(status_code=400, detail="连接码无效、已使用或已过期，请在另一台设备重新生成")
        con.execute("UPDATE pair_codes SET used_at = ? WHERE code_hash = ?", (now, digest))
        con.execute("UPDATE devices SET group_id = ?, last_seen = ? WHERE id = ?", (row["group_id"], now, device["id"]))
    return {"ok": True, "group_id": row["group_id"], "message": "设备已加入同一学习空间，正在合并学习记录"}


@app.post("/api/sync/exchange")
def sync_exchange(payload: ExchangeRequest, authorization: str | None = Header(default=None)):
    device = get_device(authorization)
    changes_size = 0
    for change in payload.changes:
        if change.key.startswith("__"):
            raise HTTPException(status_code=400, detail="同步数据键名无效")
        encoded = json.dumps(change.value, ensure_ascii=False, separators=(",", ":"))
        changes_size += len(encoded.encode("utf-8"))
        if changes_size > MAX_SYNC_BYTES:
            raise HTTPException(status_code=413, detail="本次同步数据过大，请缩短自测历史或笔记后重试")

    now = int(time.time() * 1000)
    conflicts: list[dict[str, Any]] = []
    with connect() as con:
        con.execute("BEGIN IMMEDIATE")
        for change in payload.changes:
            value_json = json.dumps(change.value, ensure_ascii=False, separators=(",", ":"))
            current = con.execute(
                "SELECT updated_at, value_json FROM sync_items WHERE group_id = ? AND item_key = ?",
                (device["group_id"], change.key),
            ).fetchone()
            if current is None or change.updated_at > current["updated_at"] or (
                change.updated_at == current["updated_at"] and value_json != current["value_json"]
            ):
                cur = con.execute(
                    "INSERT INTO sync_events(group_id, item_key, value_json, updated_at, created_at) VALUES(?,?,?,?,?)",
                    (device["group_id"], change.key, value_json, change.updated_at, now),
                )
                seq = cur.lastrowid
                con.execute(
                    "INSERT INTO sync_items(group_id, item_key, value_json, updated_at, seq) VALUES(?,?,?,?,?) "
                    "ON CONFLICT(group_id, item_key) DO UPDATE SET value_json=excluded.value_json, "
                    "updated_at=excluded.updated_at, seq=excluded.seq",
                    (device["group_id"], change.key, value_json, change.updated_at, seq),
                )
            else:
                current_item = con.execute(
                    "SELECT item_key, value_json, updated_at, seq FROM sync_items WHERE group_id = ? AND item_key = ?",
                    (device["group_id"], change.key),
                ).fetchone()
                if current_item:
                    conflicts.append({
                        "seq": current_item["seq"],
                        "key": current_item["item_key"],
                        "value": json.loads(current_item["value_json"]),
                        "updated_at": current_item["updated_at"],
                    })

        rows = con.execute(
            "SELECT seq, item_key, value_json, updated_at FROM sync_events "
            "WHERE group_id = ? AND seq > ? ORDER BY seq LIMIT 500",
            (device["group_id"], payload.cursor),
        ).fetchall()
        if rows:
            next_cursor = rows[-1]["seq"]
        else:
            next_cursor = payload.cursor
        more = con.execute(
            "SELECT 1 FROM sync_events WHERE group_id = ? AND seq > ? LIMIT 1",
            (device["group_id"], next_cursor),
        ).fetchone() is not None
    return {
        "changes": [
            {"seq": r["seq"], "key": r["item_key"], "value": json.loads(r["value_json"]), "updated_at": r["updated_at"]}
            for r in rows
        ],
        "cursor": next_cursor,
        "has_more": more,
        "conflicts": conflicts,
    }


@app.post("/api/experiences")
def create_experience(payload: ExperienceRequest, request: Request, authorization: str | None = Header(default=None)):
    enforce_rate(request, "experience-create", 8, 600)
    device = get_device(authorization)
    now = int(time.time() * 1000)
    exp_id = str(uuid.uuid4())
    nickname = payload.nickname.strip() or "匿名同学"
    content = payload.content.strip()
    with connect() as con:
        con.execute(
            "INSERT INTO experiences(id, group_id, question_id, nickname, content, created_at) VALUES(?,?,?,?,?,?)",
            (exp_id, device["group_id"], payload.question_id, nickname, content, now),
        )
    return {"id": exp_id, "question_id": payload.question_id, "nickname": nickname, "content": content, "created_at": now}


@app.get("/api/experiences")
def list_experiences(question_id: str):
    if len(question_id) > 40:
        raise HTTPException(status_code=400, detail="题目编号无效")
    with connect() as con:
        rows = con.execute(
            "SELECT id, question_id, nickname, content, created_at FROM experiences "
            "WHERE question_id = ? ORDER BY created_at DESC LIMIT 50",
            (question_id,),
        ).fetchall()
    return [dict(r) for r in rows]


@app.get("/api/experiences/mine")
def list_my_experiences(authorization: str | None = Header(default=None)):
    device = get_device(authorization)
    with connect() as con:
        rows = con.execute(
            "SELECT id, question_id, nickname, content, created_at FROM experiences "
            "WHERE group_id = ? ORDER BY created_at DESC LIMIT 500",
            (device["group_id"],),
        ).fetchall()
    return [dict(r) for r in rows]


@app.post("/api/feedback")
def create_feedback(payload: FeedbackRequest, request: Request, authorization: str | None = Header(default=None)):
    enforce_rate(request, "feedback-create", 5, 3600)
    device = get_device(authorization)
    now = int(time.time() * 1000)
    feedback_id = str(uuid.uuid4())
    with connect() as con:
        con.execute(
            "INSERT INTO feedback(id, group_id, nickname, email, category, content, created_at) VALUES(?,?,?,?,?,?,?)",
            (feedback_id, device["group_id"], payload.nickname.strip(), payload.email.strip(), payload.category, payload.content.strip(), now),
        )
    return {"ok": True, "id": feedback_id, "message": "建议已提交，感谢反馈"}


def require_admin(value: str | None) -> None:
    if not ADMIN_TOKEN:
        raise HTTPException(status_code=503, detail="尚未配置 ADMIN_TOKEN")
    if not value or not secrets.compare_digest(value, ADMIN_TOKEN):
        raise HTTPException(status_code=403, detail="无权访问")


@app.get("/api/admin/feedback")
def admin_feedback(status: str = "all", x_admin_token: str | None = Header(default=None)):
    require_admin(x_admin_token)
    with connect() as con:
        if status == "all":
            rows = con.execute("SELECT * FROM feedback ORDER BY created_at DESC LIMIT 500").fetchall()
        else:
            rows = con.execute("SELECT * FROM feedback WHERE status = ? ORDER BY created_at DESC LIMIT 500", (status,)).fetchall()
    return [dict(r) for r in rows]


@app.patch("/api/admin/feedback/{feedback_id}")
def update_feedback_status(feedback_id: str, payload: FeedbackStatusRequest, x_admin_token: str | None = Header(default=None)):
    require_admin(x_admin_token)
    with connect() as con:
        cur = con.execute("UPDATE feedback SET status = ? WHERE id = ?", (payload.status, feedback_id))
        if cur.rowcount == 0:
            raise HTTPException(status_code=404, detail="反馈不存在")
    return {"ok": True, "id": feedback_id, "status": payload.status}


@app.get("/", include_in_schema=False)
def index():
    path = WEB_DIR / "index.html"
    if not path.exists():
        raise HTTPException(status_code=500, detail="前端文件不存在")
    return FileResponse(path, media_type="text/html; charset=utf-8")


@app.get("/bank.json", include_in_schema=False)
def bank():
    path = WEB_DIR / "bank.json"
    if not path.exists():
        raise HTTPException(status_code=404, detail="题库文件不存在")
    return FileResponse(path, media_type="application/json; charset=utf-8")


@app.exception_handler(HTTPException)
async def http_error(request: Request, exc: HTTPException):
    return JSONResponse(status_code=exc.status_code, content={"detail": exc.detail})
