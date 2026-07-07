# infra — инфраструктура флота

Terraform (мульти-облако) + Ansible (sing-box, транспорты, firewall, health) +
скрипты ротации. Ядро ноды — **sing-box** ([ADR-002](../docs/decisions/ADR-002-singbox-core.md)).
Владелец: команда infra (тесно с `services/orchestrator`).

**Полная документация:** [`docs/infra.md`](../docs/infra.md).
**Домены мимикрии (кандидаты, не хардкод):** [`docs/mimicry-domains.md`](../docs/mimicry-domains.md).

## Раскладка

- `terraform/modules/compute/{hetzner,vultr,oci}` — провайдер-абстракция, единый
  интерфейс; добавить облако = скопировать папку.
- `terraform/modules/{entry-node,exit-node}` — раздельные роль-модули
  (провайдер-агностик); entry≠exit, могут быть в разных облаках.
- `terraform/envs/example` — референс-корень entry(Hetzner)+exit(Vultr).
- `ansible/roles/{singbox,transports,firewall,health_metrics,rotation}` + плейбуки
  `node-up`/`node-rotate`/`node-down` + `molecule/`.
- `scripts/` — `node_up.sh` / `node_rotate.sh` / `node_down.sh` (+ control-plane API).
- `ci/no-hardcode.sh` — guard «ноль хардкода».

## Команды

```bash
make node-up REGION=hel1 CLOUD=hetzner   # поднять пару entry+exit
make node-rotate NODE=<id>               # свежий IP + rekey REALITY, обновить Node
make node-down NODE=<id>                 # слить + retire + destroy

make infra-fmt infra-validate infra-syntax infra-molecule infra-nocode  # проверки
```

## Принципы (см. `../architecture.md`, [АНТИ-БЛОК] в `../CLAUDE.md`)

- Нода за минуты; одноразовые эфемерные entry; быстрая ротация IP.
- entry/exit разделены: атака на entry не палит exit.
- **Ноль хардкода** доменов мимикрии и IP — только переменные/секреты.
- Много транспортов сразу; REALITY без своего серта; hysteria2 с TCP-фолбэком;
  AmneziaWG ломает энтропийный детект WG.
