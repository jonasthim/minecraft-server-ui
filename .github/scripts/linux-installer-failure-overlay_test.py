import importlib.util
from pathlib import Path
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("linux-installer-failure-overlay.py")
SPEC = importlib.util.spec_from_file_location("linux_installer_failure_overlay", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


SOURCE = '''package main
import (
\t"context"
\t"os"
\t"os/signal"
)
func main() {
\tctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
\tdefer stop()
\tif err := run(ctx, os.Args[1:]); err != nil { os.Exit(1) }
}
'''


class FailureOverlayTests(unittest.TestCase):
    def test_inserts_only_a_serve_readiness_failure(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source = root / "main.go"
            source.write_text(SOURCE, encoding="utf-8")
            overlay, replacement = MODULE.make_overlay(source, root / "scratch")
            result = replacement.read_text(encoding="utf-8")
            self.assertEqual(result.count(MODULE.INSERTION), 1)
            self.assertIn('if len(os.Args) == 2 && os.Args[1] == "serve"', result)
            self.assertEqual(source.read_text(encoding="utf-8"), SOURCE)
            self.assertIn(str(source.resolve()), overlay.read_text(encoding="utf-8"))

    def test_refuses_ambiguous_anchor_and_existing_scratch(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source = root / "main.go"
            source.write_text(SOURCE + "\tdefer stop()\n", encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "exactly one"):
                MODULE.make_overlay(source, root / "one")
            source.write_text(SOURCE, encoding="utf-8")
            existing = root / "existing"
            existing.mkdir()
            with self.assertRaisesRegex(ValueError, "new private"):
                MODULE.make_overlay(source, existing)


if __name__ == "__main__":
    unittest.main()
