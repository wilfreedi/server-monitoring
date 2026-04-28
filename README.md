# Server Monitoring (CPU/RAM/Disk) — минимальная версия

Простой мониторинг: раз в минуту проверяет CPU/RAM/Disk и отправляет сообщение, если есть превышение порогов.
Если CPU превышен — дополнительно показывает топ 5 процессов по загрузке CPU.

## Что делает
- CPU, RAM, Disk (по всем разделам, кроме псевдо‑ФС)
- Топ 5 процессов по CPU при превышении порога (с командой)
- Отправка сообщения в VK через `POST /api/v1/vk/sendMessage`
- Запуск раз в минуту через systemd timer
- Настройка через `.env`

## Требования
- Ubuntu (systemd)
- Go 1.22+ для сборки

---

# 1) Сборка на своем ПК

## Установить Go
Скачать и установить Go: 
- Откройте [официальный сайт Go](https://go.dev/dl/) и установите версию 1.22+.
- Проверьте установку:

```bash
go version
```

## Собрать бинарник
Вариант для `linux/amd64`:

```bash
cd /Users/user/Documents/server-monitoring
GOOS=linux GOARCH=amd64 go build -o server-monitoring-linux-amd64 ./cmd/server-monitoring
```

Вариант для `linux/arm64`:

```bash
GOOS=linux GOARCH=arm64 go build -o server-monitoring-linux-arm64 ./cmd/server-monitoring
```

---

# 2) Установка на сервере через установщик

На сервере выполните:

```bash
curl -fsSL https://raw.githubusercontent.com/wilfreedi/server-monitoring/main/install.sh -o /tmp/install.sh
BINARY_URL="https://github.com/wilfreedi/server-monitoring/releases/download/1.0.0/server-monitoring-linux-amd64" bash /tmp/install.sh
```

Установщик спросит все параметры и сам:
- Скачает бинарник
- Создаст `/etc/server-monitoring.env`
- Установит systemd service + timer
- Запустит таймер

---

# 4) Проверка работы

Проверить таймер:

```bash
systemctl status server-monitoring.timer
```

Посмотреть логи запуска:

```bash
journalctl -u server-monitoring.service -f
```

Принудительный запуск один раз:

```bash
sudo systemctl start server-monitoring.service
```

---

# 5) Структура env

Установщик формирует файл `/etc/server-monitoring.env`. Пример:

```
API_URL="https://acmen.ru/api/v1/vk/sendMessage"
API_TOKEN="your_token_here"
VK_PEER_ID="2000000008"

CPU_THRESHOLD="80"
RAM_THRESHOLD="80"
DISK_THRESHOLD="80"
```

---

# Как протестировать

1. Уменьшите пороги, например до 1–5%.
2. Запустите вручную:

```bash
sudo systemctl start server-monitoring.service
```

3. Должно прийти сообщение в VK-чат (peer_id `2000000008` по умолчанию).

---

# Пример сообщения
```
Мониторинг сервера: my-host
Время: 2026-02-12 15:40:00 MSK
Проблемы:
- CPU: 92.1% (порог 80%)
- RAM: 85.3% (порог 80%)
- Диск /: 91.0% (использовано 91.0 GiB из 100.0 GiB, порог 80%)
```
