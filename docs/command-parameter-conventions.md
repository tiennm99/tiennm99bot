# Command Parameter Conventions

## Overview

Every Telegram command declares optional `Parameters` metadata. The bot reuses
that string in `/help` and Telegram's native command menu, so it must be short,
consistent, and understandable on a phone.

Treat `Parameters` as display syntax, not as a validation schema. Handlers
remain responsible for parsing and validation.

## Syntax

| Meaning | Format | Example |
|---|---|---|
| Required value | `<name>` | `<ticker>` |
| Required comma-separated values | `<name,...>` | `<option,...>` |
| Required remaining text | `<name...>` | `<text...>` |
| Optional value | `[name]` | `[date]` |
| Optional remaining text | `[name...]` | `[target...]` |
| Alternatives in an optional group | `[literal | literal <name>]` | `[users | user <username>]` |

Use only constructs required by the command. Do not introduce a formal schema
language or extra punctuation without a user-facing need.

## Naming

- Use lowercase `snake_case`.
- Prefer one descriptive noun: `<ticker>`, `<quantity>`, `<champion>`.
- Include units or currencies when they prevent ambiguity: `<vnd_amount>`,
  `<usd_to_spend>`.
- Do not add primitive types such as `<quantity:number>` or `<date:string>`.
- Keep literal subcommands bare: `users`, `user`, `cmd`.
- Put spaces around `|` in alternatives.
- Do not repeat format prose inside a placeholder.
- Show punctuation that users must type exactly when omitting it would be
  error-prone. `<ratio(owned:new)>` and `<option,...>` are approved compact
  shapes.

## Examples

```text
/stock_buy <quantity> <ticker>
/blacklist_del <text...>
/lol [date]
/trongtruonghop [target...]
/stats [users | user <username> | cmd <command_name>]
/stock_share_dividend <ratio(owned:new)> <ticker>
/random <option,...>
```

## Change Checklist

When adding or changing a command:

1. Make `Parameters` match the handler's accepted argument order.
2. Keep the parameter string single-line and concise enough for Telegram's
   command-description limit.
3. Update handler usage and error text to use the same syntax.
4. Update registration, handler, `/help`, and native-menu tests.
5. Update README or feature documentation when behavior is user-visible.
6. Follow the stats migration rules in `CLAUDE.md` when a command name changes
   or a command is deleted.

## References

- [POSIX.1-2024 utility conventions](https://pubs.opengroup.org/onlinepubs/9799919799/basedefs/V1_chap12.html)
- [GNU command-line interface standards](https://www.gnu.org/prep/standards/html_node/Command_002dLine-Interfaces.html)
- [Microsoft command-line syntax key](https://learn.microsoft.com/en-us/windows-server/administration/windows-commands/command-line-syntax-key)
- [Python argparse documentation](https://docs.python.org/3/library/argparse.html)
- [docopt usage-pattern grammar](https://github.com/docopt/docopt/blob/master/README.rst)
