#!/usr/bin/env python3
"""Critical-only nox gate.

Reads `findings.json` produced by `nox scan` plus `security/vex.json`
and exits non-zero when at least one critical finding is not waived.

Lower-severity findings stay informational so the build does not
flap on every typosquatting-detector tweak; critical = either a known
exploited CVE or a high-confidence supply-chain attack and must be
addressed (fix or waive) before the change lands.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import Any


def load_json(path: Path) -> Any:
    if not path.exists():
        print(f"sec-gate: {path} missing", file=sys.stderr)
        sys.exit(2)
    with path.open() as fh:
        return json.load(fh)


def main() -> int:
    findings_path = Path("findings.json")
    vex_path = Path("security/vex.json")
    findings_doc = load_json(findings_path)
    vex_doc = load_json(vex_path)

    findings = findings_doc.get("findings", findings_doc)
    waived = {
        s.get("_nox_fingerprint")
        for s in vex_doc.get("statements", [])
        if s.get("status") == "not_affected" and s.get("_nox_fingerprint")
    }

    blocking = [
        f
        for f in findings
        if f.get("Severity") == "critical" and f.get("Fingerprint") not in waived
    ]
    if not blocking:
        print(f"sec-gate: 0 unwaived critical findings ({len(findings)} total)")
        return 0

    print(f"sec-gate: {len(blocking)} unwaived critical finding(s):", file=sys.stderr)
    for f in blocking:
        print(
            f"  - {f.get('RuleID')} {f.get('Fingerprint','?')[:12]} "
            f"{f.get('Location',{}).get('FilePath','?')}"
            f": {f.get('Message','')}",
            file=sys.stderr,
        )
    print(
        "Add an OpenVEX waiver to security/vex.json or fix the underlying issue.",
        file=sys.stderr,
    )
    return 1


if __name__ == "__main__":
    sys.exit(main())
