APP     = userbot-promotion
BIN     = ./bin/$(APP)
MAIN    = ./cmd/bot/main.go
DB      = sqlite.db

.PHONY: build build-local run tidy clean install-service uninstall-service logs help

## ── Build ──────────────────────────────────────────────────────────────────

## build: Build binary untuk Linux amd64 (production deploy)
build:
	@mkdir -p bin
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
	go build -ldflags="-s -w" -o $(BIN) $(MAIN)
	@echo "✅ Binary siap: $(BIN)"

## build-local: Build binary untuk OS saat ini (development)
build-local:
	@mkdir -p bin
	CGO_ENABLED=1 go build -o $(BIN) $(MAIN)
	@echo "✅ Binary lokal siap: $(BIN)"

## run: Jalankan langsung via go run (development)
run:
	CGO_ENABLED=1 go run $(MAIN)

## tidy: Sinkronisasi go.mod dan go.sum
tidy:
	go mod tidy

## clean: Hapus binary hasil build
clean:
	rm -rf bin/
	@echo "🧹 Bersih"

## ── Deploy ─────────────────────────────────────────────────────────────────

## install-service: Install dan aktifkan systemd service (jalankan sebagai root)
## Prasyarat: binary sudah di-build dan user 'userbot' sudah dibuat
##   useradd -r -s /bin/false userbot
##   mkdir -p /opt/userbot-promotion
##   chown userbot:userbot /opt/userbot-promotion
install-service: build
	@echo "📦 Install systemd service..."
	cp $(BIN) /usr/local/bin/$(APP)
	cp deploy/userbot-promotion.service /etc/systemd/system/
	systemctl daemon-reload
	systemctl enable $(APP)
	@echo "✅ Service terinstall."
	@echo "   Jalankan: systemctl start $(APP)"
	@echo "   Lihat log: journalctl -u $(APP) -f"

## uninstall-service: Hentikan dan hapus systemd service
uninstall-service:
	systemctl stop $(APP) || true
	systemctl disable $(APP) || true
	rm -f /etc/systemd/system/$(APP).service
	rm -f /usr/local/bin/$(APP)
	systemctl daemon-reload
	@echo "🗑️  Service dihapus"

## logs: Tampilkan log service secara real-time
logs:
	journalctl -u $(APP) -f

## ── Help ───────────────────────────────────────────────────────────────────

## help: Tampilkan daftar target yang tersedia
help:
	@echo "Penggunaan: make <target>"
	@echo ""
	@grep -E '^## ' Makefile | sed 's/^## /  /'
