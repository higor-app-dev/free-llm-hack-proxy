# 🔓 Free LLM Hack Proxy

Proxy para acessar LLMs gratuitamente via APIs públicas — um laboratório de engenharia reversa, bypass de rate limits e automação de inferência.

> ⚠️ **Propósito educacional.** Este projeto é um laboratório para estudo de engenharia reversa de APIs web, protocolos HTTP, rate limiting, e técnicas de proxy. Use com responsabilidade.

## Stack

- **Linguagem:** Go 1.22+
- **Proxy HTTP:** net/http padrão
- **Browser automation:** Playwright via chromedp
- **Banco:** SQLite via modernc.org/sqlite

## Estrutura

```
free-llm-hack-proxy/
├── cmd/
│   └── main.go              — entrypoint principal
├── internal/
│   ├── api/                 — handlers HTTP (OpenAI-compatible)
│   ├── browser/             — automação de navegador (chromedp)
│   ├── config/              — parsing de config (flags, env)
│   ├── providers/           — adapters por provider (DeepSeek, MiMo, etc.)
│   └── session/             — gerenciamento de sessões browser
├── .env.example
├── go.mod
├── Makefile
└── README.md
```

## Quickstart

```bash
# build
make build

# run
make run

# tests
make test
```

## Roadmap

- [ ] Descoberta de endpoints gratuitos
- [ ] Proxy HTTP transparente (OpenAI-compatible)
- [ ] Rate limit bypass
- [ ] Cache inteligente de respostas
- [ ] Dashboard de monitoramento
