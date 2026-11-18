"""Small API smoke test. Run from the project root: python tests/api_smoke.py"""
import os
import tempfile
import sys
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

_tmp = tempfile.TemporaryDirectory(prefix="law-study-test-")
os.environ["DATA_DIR"] = _tmp.name
os.environ["ADMIN_TOKEN"] = "test-admin-token-change-me"

from fastapi.testclient import TestClient  # noqa: E402
from backend.app import app  # noqa: E402


def main() -> None:
    with TestClient(app) as client:
        assert client.get("/api/health").json()["ok"] is True
        a = client.post("/api/anonymous", json={}).json()
        b = client.post("/api/anonymous", json={}).json()
        ah = {"Authorization": "Bearer " + a["token"]}
        bh = {"Authorization": "Bearer " + b["token"]}

        code = client.post("/api/sync/pair-code", headers=ah, json={}).json()["code"]
        assert client.post("/api/sync/join", headers=bh, json={"code": code}).status_code == 200

        pushed = client.post(
            "/api/sync/exchange",
            headers=ah,
            json={"cursor": 0, "changes": [{"key": "note:judge-19", "value": {"text": "应当 / 也可以"}, "updated_at": 100}]},
        )
        assert pushed.status_code == 200, pushed.text
        pulled = client.post("/api/sync/exchange", headers=bh, json={"cursor": 0, "changes": []})
        assert any(x["key"] == "note:judge-19" for x in pulled.json()["changes"])

        share = client.post(
            "/api/experiences",
            headers=ah,
            json={"question_id": "judge-19", "nickname": "测试用户", "content": "记住查询、询问、辨认是也可以协作。"},
        )
        assert share.status_code == 200, share.text
        assert len(client.get("/api/experiences", params={"question_id": "judge-19"}).json()) == 1
        assert len(client.get("/api/experiences/mine", headers=bh).json()) == 1

        feedback = client.post(
            "/api/feedback",
            headers=ah,
            json={"nickname": "测试用户", "category": "功能建议", "content": "测试反馈内容不少于五个字。"},
        )
        assert feedback.status_code == 200, feedback.text
        admin = client.get("/api/admin/feedback", headers={"X-Admin-Token": "test-admin-token-change-me"})
        assert admin.status_code == 200 and len(admin.json()) == 1
        assert "法律基础知识" in client.get("/").text

    print("API smoke test passed: account, device pairing, record sync, public experiences, feedback, admin endpoint, frontend route")
    _tmp.cleanup()


if __name__ == "__main__":
    main()
