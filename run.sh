go build -trimpath -ldflags='-s -w' -o bin/lawstudy ./cmd/lawstudy

sudo systemctl restart law_for_police
