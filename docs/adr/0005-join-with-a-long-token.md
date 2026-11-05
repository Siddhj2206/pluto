# Devices join a host with a long token

A device joins a host using a one-time token that the person pastes or scans. The token carries the host's public key and a secret, plus the route details when they're needed, and it works the same way on every route. Length buys simplicity: a short typed code would need a key-agreement protocol to authenticate the host, and a rendezvous service to find it over Tailcat, whose addresses are already over 200 characters.

## Considered options

- **A short typed code with a PAKE.** Nicer to read aloud, but it adds cryptography to get right, and over Tailcat it needs a service that Pluto would have to run.

## Consequences

- Moving a token between devices needs a copy channel, such as a password manager, chat or a QR code, and a token in chat is secret until used or expired. So tokens are one-use and expire quickly.
- A person can't type a token by hand from a screen.

**Revisit when:** real users regularly fail to get a token onto a new device, or Pluto gains a rendezvous mechanism for another reason.
