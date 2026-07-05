# Security Policy

## Reporting a vulnerability

Email **security@firstops.dev** (or open a private security advisory on GitHub).
Please do not open a public issue for security reports. We aim to acknowledge
within 3 business days.

## Scope — what we treat as security-class

whittle rewrites the tool output an AI agent reads. Beyond conventional
vulnerabilities, we treat these as high severity:

- **Fidelity violations**: any input where whittle changes the *meaning* of an
  output a lossless path claims to preserve, or where code/config reaches the
  lossy prose model. (See GUARANTEES.md.)
- **Retrieval integrity**: `whittle_get` returning content that is not the exact
  original for a given id.
- **Fail-open breaches**: any path where a whittle failure breaks a tool call
  instead of passing the original through.

All three are pinned by tests; a reproducer that defeats them is a security bug.
