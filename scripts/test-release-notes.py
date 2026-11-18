#!/usr/bin/env python3
"""Offline publication safety tests; no command may reach GitHub or Docker."""
import argparse
import copy
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("release_notes", Path(__file__).with_name("release-notes.py"))
notes = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(notes)


class PublicationTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.trusted = self.root / "trusted"
        self.output = self.root / "output"
        self.trusted.mkdir()
        self.output.mkdir()
        self.original = "Release introduction.\n\n## Highlights\nOld summary.\n\n## Artifacts\nKeep these exact download instructions.\n\nFull changelog: https://github.com/docker/sandbox-kit-spec/compare/v3.1.0...v3.2.0\n"
        self.current = dict(id=123, tag_name="v3.2.0", draft=False, prerelease=False,
                            published_at="2026-01-01T00:00:00Z", updated_at="2026-01-01T00:00:00Z",
                            body=self.original, html_url="https://github.com/docker/sandbox-kit-spec/releases/tag/v3.2.0")
        self.snapshot = dict(release=copy.deepcopy(self.current), commit="a" * 40,
                             base="v3.1.0", base_commit="b" * 40)
        self.draft = ("Release introduction.\n\n## Highlights\n"
                      "The resolver now handles a documented compatibility improvement, with public evidence.\n\n"
                      "## Compatibility\nExisting Kit descriptors continue to work.\n\n"
                      "## Artifacts\nKeep these exact download instructions.\n\n"
                      "Full changelog: https://github.com/docker/sandbox-kit-spec/compare/v3.1.0...v3.2.0\n")
        self.evidence = dict(tag="v3.2.0", commit="a" * 40, base="v3.1.0", base_commit="b" * 40,
                             complete=True, gaps=[], claims=[dict(claim="Resolver improvement", sources=["https://github.com/docker/sandbox-kit-spec/pull/42"])])
        self.write_inputs()
        self.args = argparse.Namespace(tag="v3.2.0", trusted=self.trusted, output=self.output, publish=True)
        # Default-deny every external command, even if future code adds another path.
        self.commands = patch.object(notes.subprocess, "check_output", side_effect=AssertionError("unexpected external command"))
        self.commands.start()
        self.addCleanup(self.commands.stop)

    def write_inputs(self):
        (self.trusted / "snapshot.json").write_text(json.dumps(self.snapshot))
        (self.output / "release-notes.md").write_text(self.draft)
        (self.output / "evidence.json").write_text(json.dumps(self.evidence))

    def exercise(self, current=None, commits=None, after=None, fails=False):
        current = copy.deepcopy(self.current if current is None else current)
        after = copy.deepcopy(current if after is None else after)
        if after.get("body") == self.original:
            after["body"] = self.draft
        releases = [current, after]
        with patch.object(notes, "release", side_effect=releases), \
             patch.object(notes, "remote_commit", side_effect=commits or ["a" * 40, "b" * 40, "a" * 40]), \
             patch.object(notes.subprocess, "run") as command:
            if fails:
                with self.assertRaises((ValueError, KeyError, TypeError, OSError, json.JSONDecodeError)):
                    notes.check(self.args)
            else:
                notes.check(self.args)
            return command.call_args_list

    def test_success_publishes_once_from_trusted_file_with_exact_bytes(self):
        calls = self.exercise()
        self.assertEqual(len(calls), 1)
        argv = calls[0].args[0]
        self.assertEqual(argv[:4], ["gh", "release", "edit", "v3.2.0"])
        published = Path(argv[argv.index("--notes-file") + 1])
        self.assertEqual(published.parent, self.trusted)
        self.assertEqual(published.read_bytes(), self.draft.encode())
        self.assertEqual((self.trusted / "agent-output" / "evidence.json").read_bytes(), (self.output / "evidence.json").read_bytes())

    def test_success_preserves_crlf_bytes_in_model_text(self):
        self.draft = self.draft.replace("with public evidence.\n", "with public evidence.\r\n")
        (self.output / "release-notes.md").write_bytes(self.draft.encode())
        calls = self.exercise()
        argv = calls[0].args[0]
        published = Path(argv[argv.index("--notes-file") + 1])
        self.assertIn(b"\r\n", published.read_bytes())
        self.assertEqual(published.read_bytes(), self.draft.encode())
        self.assertEqual((self.trusted / "agent-output" / "release-notes.md").read_bytes(), self.draft.encode())

    def test_concurrent_release_changes_never_publish(self):
        changes = dict(id=124, tag_name="v3.2.1", draft=True, prerelease=True,
                       published_at="2026-02-01", updated_at="2026-02-01", body="Maintainer changed notes")
        for field, value in changes.items():
            with self.subTest(field=field):
                current = dict(self.current, **{field: value})
                self.assertEqual(self.exercise(current=current, fails=True), [])

    def test_moved_release_or_base_tag_never_publishes(self):
        for commits in (["c" * 40, "b" * 40], ["a" * 40, "c" * 40]):
            with self.subTest(commits=commits):
                self.assertEqual(self.exercise(commits=commits, fails=True), [])

    def test_range_and_incomplete_evidence_never_publish(self):
        changes = dict(tag="v3.9.0", commit="c" * 40, base="v3.0.0", base_commit="c" * 40,
                       complete=False, gaps=["missing manifest"], claims=[])
        for field, value in changes.items():
            with self.subTest(field=field):
                original = copy.deepcopy(self.evidence)
                self.evidence[field] = value
                self.write_inputs()
                self.assertEqual(self.exercise(fails=True), [])
                self.evidence = original

    def test_invalid_claims_never_publish(self):
        for claims in ([{}], ["unsupported"], [{"claim": "claim", "sources": []}],
                       [{"claim": "claim", "sources": "not a source list"}]):
            with self.subTest(claims=claims):
                self.evidence["claims"] = claims
                self.write_inputs()
                self.assertEqual(self.exercise(fails=True), [])

    def test_missing_invalid_or_empty_model_files_never_publish(self):
        for filename, content in (("release-notes.md", ""), ("evidence.json", ""),
                                  ("evidence.json", "{broken")):
            with self.subTest(filename=filename, content=content):
                self.write_inputs()
                (self.output / filename).write_text(content)
                self.assertEqual(self.exercise(fails=True), [])

    def test_missing_model_file_never_publishes(self):
        for filename in ("release-notes.md", "evidence.json"):
            with self.subTest(filename=filename):
                self.write_inputs()
                (self.output / filename).unlink()
                self.assertEqual(self.exercise(fails=True), [])

    def test_output_directory_symlink_never_publishes(self):
        linked = self.root / "linked-output"
        linked.symlink_to(self.output, target_is_directory=True)
        self.args.output = linked
        self.assertEqual(self.exercise(fails=True), [])

    def test_output_symlinks_and_oversize_never_publish(self):
        for filename in ("release-notes.md", "evidence.json"):
            for kind in ("symlink", "oversize"):
                with self.subTest(filename=filename, kind=kind):
                    path = self.output / filename
                    path.unlink(missing_ok=True)
                    self.write_inputs()
                    if kind == "symlink":
                        target = self.root / "linked-content"
                        target.write_bytes(path.read_bytes())
                        path.unlink()
                        path.symlink_to(target)
                    else:
                        path.write_text("x" * 125001)
                    self.assertEqual(self.exercise(fails=True), [])
                    path.unlink()

    def test_removed_or_modified_protected_sections_never_publish(self):
        for changed in (self.draft.replace("Release introduction.", "Changed introduction."),
                        self.draft.replace("Keep these exact download instructions.", "Changed downloads."),
                        self.draft.replace("Keep these exact download instructions.", "Keep these exact download instructions.\nInjected download instructions.")):
            with self.subTest(draft=changed):
                (self.output / "release-notes.md").write_text(changed)
                self.assertEqual(self.exercise(fails=True), [])

    def test_unrelated_agent_files_are_not_staged(self):
        (self.output / "secrets").symlink_to(self.root)
        (self.output / "execute-me.sh").write_text("exit 99")
        self.exercise()
        self.assertEqual({p.name for p in (self.trusted / "agent-output").iterdir()}, {"release-notes.md", "evidence.json", "release-notes.diff"})

    def test_postwrite_mismatch_is_error_without_retry(self):
        after = dict(self.current, body="Server returned different notes")
        self.assertEqual(len(self.exercise(after=after, fails=True)), 1)

    def test_postwrite_identity_mismatch_is_error_without_retry(self):
        for field, value in (("id", 999), ("prerelease", True), ("tag_name", "v3.9.0")):
            with self.subTest(field=field):
                after = dict(self.current, **{field: value})
                self.assertEqual(len(self.exercise(after=after, fails=True)), 1)

    def test_publish_command_failure_is_not_retried(self):
        with patch.object(notes, "release", return_value=self.current), \
             patch.object(notes, "remote_commit", side_effect=["a" * 40, "b" * 40]), \
             patch.object(notes.subprocess, "run", side_effect=subprocess.CalledProcessError(1, "gh")) as command:
            with self.assertRaises(subprocess.CalledProcessError):
                notes.check(self.args)
            self.assertEqual(command.call_count, 1)

    def test_tag_moved_during_write_is_error_without_retry(self):
        self.assertEqual(len(self.exercise(commits=["a" * 40, "b" * 40, "c" * 40], fails=True)), 1)

    def test_requested_tag_mismatch_never_publishes(self):
        self.args.tag = "v3.9.0"
        self.assertEqual(self.exercise(fails=True), [])

    def test_preview_never_calls_external_commands(self):
        self.args.publish = False
        with patch.object(notes, "release", side_effect=AssertionError("preview contacted GitHub")), \
             patch.object(notes.subprocess, "run") as command:
            notes.check(self.args)
            command.assert_not_called()


class ApiTests(unittest.TestCase):
    def test_paginated_list_endpoint(self):
        with patch.object(notes.subprocess, "check_output", return_value=' \n[{"number": 1}]\t\n[]\r\n[{"number": 2}]  ') as command:
            self.assertEqual(notes.api("repos/example/pulls", paginate=True), [{"number": 1}, {"number": 2}])
            self.assertEqual(command.call_args.args[0], ("gh", "api", "repos/example/pulls", "--paginate"))

    def test_paginated_workflow_and_jobs_object_endpoints(self):
        for collection in ("workflow_runs", "jobs"):
            with self.subTest(collection=collection), patch.object(notes.subprocess, "check_output", return_value="\n".join(json.dumps({collection: items}) for items in ([{"id": 1}], [], [{"id": 2}]))):
                self.assertEqual(notes.api("repos/example/actions", paginate=True, collection=collection), [{"id": 1}, {"id": 2}])

    def test_malformed_later_page_is_rejected(self):
        with patch.object(notes.subprocess, "check_output", return_value='[{"id": 1}]\n{broken'):
            with self.assertRaises(json.JSONDecodeError):
                notes.api("repos/example/releases", paginate=True)

    def test_annotated_tags_resolve_to_commit(self):
        with patch.object(notes, "api", side_effect=[{"object": {"type": "tag", "sha": "annotation"}}, {"object": {"type": "commit", "sha": "a" * 40}}]):
            self.assertEqual(notes.remote_commit("v3.2.0"), "a" * 40)

    def test_unresolvable_tags_fail_closed(self):
        with patch.object(notes, "api", return_value={"object": {"type": "tree", "sha": "a" * 40}}):
            with self.assertRaises(ValueError):
                notes.remote_commit("v3.2.0")


if __name__ == "__main__":
    unittest.main()
