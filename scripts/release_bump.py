"""Read untrusted label data as JSON, never as shell source."""
import json
import os


def release_bump(raw):
    labels = json.loads(raw)
    if not isinstance(labels, list) or not all(isinstance(label, str) for label in labels):
        raise ValueError("labels must be a JSON array of strings")
    for level in ("major", "minor"):
        if "release:" + level in labels:
            return level
    return "patch"


if __name__ == "__main__":
    print("level=" + release_bump(os.environ["PR_LABELS_JSON"]))
