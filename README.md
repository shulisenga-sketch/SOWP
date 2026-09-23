# SOWP - Service Ordering Web Portal

Muundo wa awali wa mradi wa Go web portal kwa maombi ya huduma, malipo ya manual,
mawasiliano ya ndani ya portal, na delivery ya mafaili.

## Project structure

```text
SOWP/
├── cmd/
│   └── server/              # Entry point ya HTTP server
├── internal/
│   ├── auth/                # Password hashing, sessions, auth middleware
│   ├── config/              # Environment/configuration loading
│   ├── database/             # SQLite connection, migrations, repositories
│   ├── handlers/             # HTTP handlers za customer na admin portals
│   ├── models/               # Domain structs na status constants
│   ├── notifications/       # In-app notification creation/read logic
│   ├── storage/              # File validation na local filesystem storage
│   └── web/                  # Template rendering na shared view data
├── migrations/
│   └── 001_initial.sql       # Schema ya database ya kuanzia
├── templates/
│   ├── layouts/              # Base layout, header, navigation
│   ├── auth/                 # Login na register
│   ├── customer/             # Dashboard, order form, order details
│   ├── admin/                # Admin dashboard, payment/work management
│   └── partials/             # Flash messages, notifications, comments
├── static/
│   ├── css/                  # CSS ya responsive UI
│   ├── js/                   # Vanilla JavaScript na Fetch API
│   └── images/               # Public UI assets zisizo confidential
├── storage/
│   ├── supporting/           # Mafaili ya kusaidia ya customer
│   ├── payment-proofs/       # Proofs za malipo
│   └── deliverables/         # Kazi za mwisho
├── tests/                    # Integration na end-to-end tests
├── .env.example              # Mfano wa configuration, bila secrets
├── go.mod
└── README.md
```

## Database design

`migrations/001_initial.sql` ina tables hizi:

- `users`: customers/admins; password ni hash pekee, si plain text.
- `sessions`: secure server-side sessions zinazounganishwa na user.
- `services`: aina za huduma kama Field Report, Research Proposal, Full Research, HESLB, au maombi ya vyuo.
- `orders`: ombi, owner, huduma, maelezo, status, na revision note.
- `files`: metadata ya uploads zote; content inawekwa kwenye `storage/` na si public web root.
- `payments`: payment request moja kwa order na manual verification state.
- `comments`: mawasiliano ya customer/admin ndani ya order.
- `notifications`: bell notifications na read/unread state.

Fedha zinawekwa kama `amount_cents` (TZS) ili kuepuka floating-point rounding.
Upload size ya juu ni 15 MiB (`15728640` bytes); handler ya Go itaongeza validation ya
extension, MIME sniffing, na random stored filename kabla ya kuandika file.

## Payment methods za admin

## MySQL (XAMPP)

SQLite ndiyo default. Ili data mpya ihifadhiwe kwenye MySQL/MariaDB ya XAMPP,
create database kwanza kwenye phpMyAdmin, kisha run server kwa environment hizi:

```powershell
$env:SOWP_DATABASE_DRIVER = "mysql"
$env:SOWP_DATABASE_DSN = "root:@tcp(127.0.0.1:3306)/sowp?charset=utf8mb4"
go run ./cmd/server
```

Badilisha `root`, password, host, port, au database name kulingana na XAMPP yako.
Server itatumia migrations za `migrations/mysql/` na itatengeneza tables yenyewe.
Data iliyopo kwenye `data/sowp.db` haitahamishwa moja kwa moja; SQLite na MySQL ni
databases tofauti, hivyo export/import inahitajika kama unataka history iliyopo pia.

Admin anaweza kusimamia namba za malipo kupitia endpoints hizi:

- `GET /api/admin/payment-methods`: kuona methods zote.
- `POST /api/admin/payment-methods`: kuongeza Lipa Namba, simu, au akaunti ya benki.
- `PUT /api/admin/payment-methods/{methodID}`: kubadilisha taarifa za akaunti.
- `DELETE /api/admin/payment-methods/{methodID}`: kuizima bila kufuta history.

Customer anaona methods active kupitia `GET /api/payment-methods`. Wakati admin
anatuma payment request, anaweza kuweka `payment_method_id`; customer ataona
`account_number`, `account_name`, na instructions kupitia
`GET /api/orders/{orderID}/payment`.

## Status flow

```text
pending -> in_progress -> payment_requested -> payment_verification -> completed
                                      \-> revision_needed -> in_progress
```

`cancelled` inaweza kutumika na admin pale order inapofungwa bila kukamilika.