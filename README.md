# TamizChat — بک‌اند

بک‌اند self-hosted پروژهٔ TamizChat، نوشته‌شده با Go.

## اجرا

```bash
go build -o tamizchat ./cmd/tamizchat
```

```bash
./tamizchat run
```

سرور روی `:8080` بالا می‌آید و دیتابیس را در `data/tamizchat.db` می‌سازد.

## پنل مدیریت

بدون هیچ آرگومانی، پنل تعاملی باز می‌شود (سبک x-ui):

```bash
./tamizchat
```

از داخل پنل می‌توانی نام سرور، رمز ورود، پورت، تنظیمات روم‌ها، آپلود، LiveKit و
سطح لاگ را ببینی و تغییر دهی. هیچ فایل `.env` وجود ندارد؛ همهٔ تنظیمات در همان
فایل SQLite ذخیره می‌شوند.

> در حال حاضر تغییرات پنل روی سرورِ در حال اجرا پس از ری‌استارت اعمال می‌شود.
> کنترل زندهٔ سرور در فاز ۱۰ اضافه می‌شود.

## پرچم‌ها

| پرچم | پیش‌فرض | توضیح |
|------|---------|-------|
| `-db` | `data/tamizchat.db` (یا `TAMIZCHAT_DB`) | مسیر فایل دیتابیس |
| `-log` | `info` | سطح لاگ اولیه: `debug\|info\|warn\|error` |

## اندپوینت‌های فعلی

| متد | مسیر | توضیح |
|-----|------|-------|
| GET | `/healthz` | سلامت سرویس و uptime |
| GET | `/api/v1/server-info` | اطلاعات عمومی سرور برای کلاینت پیش از اتصال |
| GET | `/ws` | اتصال WebSocket کلاینت (handshake با پیام `hello`) |
| POST | `/api/v1/upload` | آپلود فایل با تیکت |
| GET | `/api/v1/file/{id}` | دانلود فایل با توکن کوتاه‌عمر |
| GET | `/api/v1/bot-stream/{id}` | فایل موسیقی بات (فقط برای LiveKit Ingress) |
| POST | `/api/v1/livekit/webhook` | وب‌هوک LiveKit (با تأیید امضا) |

## تست

```bash
go test ./...
```

## بیلد

```bash
make build      # باینری برای همین سیستم
make release    # لینوکس/ویندوز/مک، amd64 و arm64
make check      # vet + تست
```

## مستندات

- [راهنمای راه‌اندازی برای اپراتور](docs/DEPLOY.md)

- [پروتکل کلاینت ↔ سرور](docs/PROTOCOL.md)
- [نقشهٔ راه و فازبندی](docs/ROADMAP.md)
- [حافظهٔ پروژه و تصمیم‌های معماری](MEMORY.md)
