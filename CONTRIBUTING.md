# Contributing

Thanks for taking a look. This is an **alpha, AI-generated** project — careful human review and
real-hardware testing are exactly what it needs most.

## Ways to help

- **Report bugs** with logs (`journalctl -u sipvoice -n 200`), your distro/kernel, CPU architecture and
  whether the call was placed from a softphone or a real phone.
- **Test on hardware you have**: ARM boards, FXS gateways, fax machines, unusual SIP phones.
- **Review code**: point out anything that looks wrong, unsafe or unnecessary complex.
- **Improve docs**: the README is bilingual (English + 简体中文); fixes to either are welcome.

## Development setup

```bash
git clone https://github.com/USER/sipvoice.git
cd sipvoice
go test ./...        # unit tests (pure Go, no external services)
go vet ./...
make build           # binary for your platform
make release         # amd64 + arm64 tarballs
```

To try it without a phone, run the agent with the mock AI:

```bash
go run ./cmd/voicebridge -mode mockai -mock-listen 127.0.0.1:8090 &
go run ./cmd/voicebridge -mode agent -config deploy/config.test.json
```

`-mode calltest` places a real SIP call to a running Asterisk, which is how the end-to-end audio path
is normally verified.

## Pull requests

- Keep changes focused; one topic per PR.
- Run `go vet ./...` and `go test ./...` before pushing.
- Match the existing style: comments and log output are in Chinese, public docs are bilingual.
- If you change behaviour, update `CHANGELOG.md` and, if relevant, both READMEs.
- If your change touches deployment (`deploy/*.sh`), say how you tested it on a clean machine.

## Commit messages

Short, imperative, optionally prefixed:

```
feat: add VAD sensitivity preset
fix: avoid double reply when barge-in happens during greeting
docs: clarify TLS certificate import
chore: bump version to 1.4.0
```

## Security

Never open a public issue for a vulnerability — see [SECURITY.md](SECURITY.md).

## License of contributions

By submitting a pull request you agree that your contribution is licensed under the MIT license of
this repository.
