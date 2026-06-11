BINARY := lgtv2pc
PREFIX := /usr/local
CONFDIR := /etc/lgtv2pc

.PHONY: build install uninstall pair clean

build:
	go build -o $(BINARY) .

install: build
	install -Dm755 $(BINARY) $(PREFIX)/bin/$(BINARY)
	install -dm755 $(CONFDIR)
	install -Dm644 systemd/lgtv2pc.service /etc/systemd/system/lgtv2pc.service
	systemctl daemon-reload
	@echo
	@echo "Siguiente paso: lanza el onboarding (la TV debe estar encendida):"
	@echo "  sudo $(PREFIX)/bin/$(BINARY) -setup"
	@echo "Luego:  sudo systemctl enable --now lgtv2pc"

# Onboarding interactivo: localiza la TV, empareja y crea la config.
setup: build
	sudo ./$(BINARY) -setup -config $(CONFDIR)/config.json

# Re-empareja sobre una config ya existente.
pair: build
	sudo ./$(BINARY) -pair -config $(CONFDIR)/config.json

uninstall:
	systemctl disable --now lgtv2pc 2>/dev/null || true
	rm -f /etc/systemd/system/lgtv2pc.service
	rm -f $(PREFIX)/bin/$(BINARY)
	systemctl daemon-reload
	@echo "Config conservada en $(CONFDIR) (bórrala a mano si quieres)"

clean:
	rm -f $(BINARY)
