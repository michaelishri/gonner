import json
import unittest
from release_bump import release_bump


class ReleaseBumpTests(unittest.TestCase):
    def test_exact_membership_and_priority(self):
        for labels, expected in [([], "patch"), (["release:minor"], "minor"),
                                 (["release:minor", "release:major"], "major"),
                                 (["xrelease:major", "release:minor suffix"], "patch")]:
            self.assertEqual(release_bump(json.dumps(labels)), expected)

    def test_shell_and_newline_payloads_remain_data(self):
        labels = ["'; touch /tmp/gonner-label-injected; #", "$(exit 99)",
                  "`exit 99`", "\nlevel=major", '"release:major"']
        self.assertEqual(release_bump(json.dumps(labels)), "patch")

    def test_rejects_non_array(self):
        with self.assertRaises(ValueError):
            release_bump('{"release:major": true}')

    def test_cli_treats_injection_payload_as_environment_data(self):
        import os
        from pathlib import Path
        import subprocess
        import sys
        import tempfile
        with tempfile.TemporaryDirectory() as directory:
            marker = Path(directory) / "injected"
            payload = "'; touch " + str(marker) + "; #"
            environment = dict(os.environ, PR_LABELS_JSON=json.dumps([payload, "release:minor"]))
            result = subprocess.run([sys.executable, str(Path(__file__).with_name("release_bump.py"))],
                                    env=environment, text=True, capture_output=True, check=True)
            self.assertEqual(result.stdout, "level=minor\n")
            self.assertFalse(marker.exists())
