"""Joins blinded assessments with arm labels and trace measurements -> results.json."""
import json
from pathlib import Path

here = Path(__file__).parent
assessments = {x["opaque_id"]: x for x in json.loads((here / "assessments.json").read_text())}
key = json.loads((here / "blind-key.json").read_text())
measured = {r["trial"]: r for r in json.loads((here / "measurements.json").read_text())}
rows = []
for opaque, trial in key.items():
    rows.append({**measured[trial], "opaque_id": opaque, "grade": assessments[opaque]})
rows.sort(key=lambda r: (r["task"], r["arm"]))
(here / "results.json").write_text(json.dumps(rows, indent=2) + "\n")
for r in rows:
    g = r["grade"]
    print(r["task"], r["arm"], "success" if g["strict_success"] else "FAIL", f'{g["facts_correct"]}/{g["facts_required"]}',
          "abst", g["abstained_correctly"], "failcond", g["fail_condition_hit"], "cand_as_verified", g["candidate_presented_as_verified"],
          "xmention", g["cross_project_mention"], "xleak", g["cross_project_leak"], "|", g["note"][:120])
