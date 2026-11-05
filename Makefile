MODULES := runtime services/tavern-core services/egress
export GOTOOLCHAIN ?= local

.PHONY: test vet fmt fmt-check build up down logs

test: vet
	@for m in $(MODULES); do echo "== $$m"; (cd $$m && go test ./...) || exit 1; done

vet:
	@for m in $(MODULES); do (cd $$m && go vet ./...) || exit 1; done

fmt:
	gofmt -w runtime services

fmt-check:
	@out=$$(gofmt -l runtime services); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

build:
	docker compose build

up:
	docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f --tail=100
