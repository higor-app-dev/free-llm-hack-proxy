# 🔓 Free LLM Hack Proxy

Proxy para acessar LLMs gratuitamente via APIs públicas — um laboratório de engenharia reversa, bypass de rate limits e automação de inferência.

> ⚠️ **Propósito educacional.** Este projeto é um laboratório para estudo de engenharia reversa de APIs web, protocolos HTTP, rate limiting, e técnicas de proxy. Use com responsabilidade.

## Stack

- **Linguagem:** Python 3.11+
- **Proxy HTTP:** aiohttp / httpx
- **CLI / Dashboard:** typer + rich
- **Cache:** SQLite (local) / Redis (opcional)

## Estrutura

```
free-llm-hack-proxy/
├── src/
│   ├── __init__.py
│   ├── proxy/          — servidor proxy principal
│   ├── providers/      — adapters por provider (HuggingFace, Groq, etc.)
│   ├── utils/          — ferramentas compartilhadas
│   └── cli/            — interface de linha de comando
├── tests/
├── config/
├── .gitignore
└── README.md
```

## Roadmap

- [ ] Descoberta de endpoints gratuitos
- [ ] Proxy HTTP transparente
- [ ] Rate limit bypass
- [ ] Cache inteligente de respostas
- [ ] Dashboard de monitoramento
