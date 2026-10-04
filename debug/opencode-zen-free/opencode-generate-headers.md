# OpenCode Header Generation

Referensi: `sst/opencode` — `packages/opencode/src/id/id.ts`, `packages/core/src/project.ts`, `packages/core/src/util/hash.ts`.

## 1. Session ID (`x-opencode-session`, `x-session-affinity`, `x-session-id`)

Ketiganya berisi nilai yang sama: `<prefix>_ + 26 char`.

```ts
// packages/opencode/src/id/id.ts
const LENGTH = 26;
function randomBase62(length: number): string {
  const chars = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
  const bytes = randomBytes(length);
  let out = "";
  for (let i = 0; i < length; i++) out += chars[bytes[i] % 62];
  return out;
}
function create(prefix: string, direction: "descending"|"ascending", timestamp?: number): string {
  const now0 = BigInt(timestamp ?? Date.now()) * BigInt(0x1000) + BigInt(counter++);
  const now = direction === "descending" ? ~now0 : now0;
  const timeBytes = Buffer.alloc(6);
  for (let i = 0; i < 6; i++)
    timeBytes[i] = Number((now >> BigInt(40 - 8 * i)) & BigInt(0xff));
  return prefix + "_" + timeBytes.toString("hex") + randomBase62(LENGTH - 12);
}
// session = create("ses", "descending")
```

Struktur `ses_<12 hex><14 base62>`:

* 12 hex = `~(Date.now()*0x1000 + counter)` dipotong 48-bit. Bisa di-decode jadi timestamp ± counter, tapi ambigu modulo 2^36 ms (~2,17 tahun).
* 14 char = acak base62, tidak ada arti.

Python:

```python
import secrets, time
MASK = (1 << 48) - 1
CHARS = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
ts = int(time.time() * 1000)
now = (ts * 0x1000 + 1) & MASK
inv = (~now) & MASK
sid = f"ses_{inv:012x}" + "".join(secrets.choice(CHARS) for _ in range(14))
headers = {"x-opencode-session": sid, "x-session-affinity": sid, "x-session-id": sid}
```

## 2. `traceparent` (W3C Trace Context)

Format: `{version}-{trace-id}-{span-id}-{flags}`

* `version` = `00`
* `trace-id` = 16 byte acak = 32 hex, tidak boleh nol semua
* `span-id` = 8 byte acak = 16 hex, tidak boleh nol semua
* `flags` = `01` (sampled)

```python
import secrets
trace = secrets.token_hex(16)
span = secrets.token_hex(8)
tp = f"00-{trace}-{span}-01"
```

## 3. `b3` (Zipkin B3 single header)

Format: `{trace-id}-{span-id}-{sampled}-{parent-span-id}`

* `trace-id`, `span-id` = sama dengan `traceparent` di atas
* `sampled` = `1`
* `parent-span-id` = 8 byte acak = 16 hex

```python
parent = secrets.token_hex(8)
b3 = f"{trace}-{span}-1-{parent}"
```

`traceparent` + `b3` sengaja dikirim dobel untuk kompatibilitas backend W3C vs Zipkin.

## 4. `x-opencode-project`

40 hex = SHA1 satu arah (`Hash.fast = sha1`), bukan encoding. Sumber di `packages/core/src/project.ts:resolve()`:

1. Coba remote git → `sha1("git-remote:<host>/<path>")`
2. Kalau tidak ada, pakai cache file `<commonDir>/opencode` atau root-commit git
3. Kalau bukan repo, `"global"`

Normalisasi `host/path` di `parts()`:

* host di-lowercase
* path buang `/` depan, akhiran `.git`, `/` belakang

```python
import hashlib
def project_id(host: str, path: str) -> str:
    p = path.lstrip("/").removesuffix(".git").rstrip("/")
    norm = f"{host.lower()}/{p}"
    return hashlib.sha1(f"git-remote:{norm}".encode()).hexdigest()
# contoh: project_id("github.com", "user/repo")
```

Nilai seperti `9dc20b006142d921ea0474629eab5930bc3cea00` tidak bisa di-decode, hanya bisa diverifikasi ulang jika remote-nya diketahui.
