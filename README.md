# Bonsai Tunnel

[🇷🇺 Русский](#russian) | [🇺🇸 English](#english)

---

<a name="russian"></a>
## Описание (RU)

Легковесный **Reverse TCP туннель** на Go для проброса локальных портов через внешний сервер. Поддерживает динамические поддомены, авторизацию и SSL-терминацию.

### Основные возможности

* **Reverse Tunneling:** Доступ к локальному сервису извне без белого IP и настройки NAT.
* **Zero-DB:** Состояние сессий хранится в `sync.Map`. Никаких логов и внешних БД.
* **Multi-Server:** Одновременное подключение клиента к нескольким нодам для избыточности.
* **Auth:** Защита поддоменов через Auth Key.
* **SSL Ready:** Нативная поддержка `X-Forwarded-Proto` для работы за Nginx/Caddy.

---

<a name="english"></a>
## Description (EN)

A lightweight **Reverse TCP Tunnel** written in Go, designed to expose local ports via an external server. It supports dynamic subdomains, authentication, and SSL termination.

### Key Features

* **Reverse Tunneling:** Access local services from the internet without a public IP or complex NAT configurations.
* **Zero-DB:** Session states are stored in-memory using `sync.Map`. No logs, no external databases required.
* **Multi-Server Support:** Connect a client to multiple nodes simultaneously for high availability and redundancy.
* **Authentication:** Secure your subdomains using custom Auth Keys.
* **SSL Ready:** Native support for `X-Forwarded-Proto` for seamless integration behind Nginx, Caddy, or other reverse proxies.
