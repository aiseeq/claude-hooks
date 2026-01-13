# Claude Code Hooks

Standalone validator hooks for Claude Code CLI. Blocks dangerous patterns and operations before they're executed.

## Features

### Validators (TIER-1 - Block Operations)

- **emergency_defaults** - Blocks "fallback" keyword in executable code (warns on `||`, `??` patterns)
- **runtime_exit** - Blocks `os.Exit()`, `log.Fatal()`, `panic()` outside of cmd/ and main.go
- **secrets** - Blocks hardcoded JWT tokens and wallet addresses

### Tools

- **bash** - Blocks dangerous commands (`--headed`, `rm -rf /`, `rm -rf ~`)
- **formatter** - Auto-formats Go files with gofmt, TS/JS with prettier (post-tool-use)
- **notifier** - Desktop notifications when Claude Code session completes

## Installation

```bash
git clone https://github.com/aiseeq/claude-hooks.git
cd claude-hooks
make install
```

This installs:
- Binary to `~/bin/claude-hooks`
- Config to `~/.claude/hooks/config.yaml`

### Configure Claude Code

Add to `~/.claude/settings.json`:

```json
{
  "hooks": {
    "PreToolUse": [
      {"matcher": "Write|Edit|MultiEdit", "hooks": [{"type": "command", "command": "$HOME/bin/claude-hooks pre-tool-use", "timeout": 5000}]},
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "$HOME/bin/claude-hooks pre-tool-use", "timeout": 3000}]}
    ],
    "PostToolUse": [
      {"matcher": "Write|Edit|MultiEdit", "hooks": [{"type": "command", "command": "$HOME/bin/claude-hooks post-tool-use", "timeout": 5000}]}
    ],
    "Stop": [
      {"matcher": "", "hooks": [{"type": "command", "command": "$HOME/bin/claude-hooks stop", "timeout": 3000}]}
    ]
  }
}
```

## Configuration

Edit `~/.claude/hooks/config.yaml` to customize validators and tools.

## Development

```bash
# Build
make build

# Run tests
make test

# Format code
make fmt
```

## Requirements

- Go 1.21+
- For formatter: gofmt (included with Go), prettier (optional, for JS/TS)
- For notifier: notify-send, canberra-gtk-play or paplay (Linux)

## License

MIT
