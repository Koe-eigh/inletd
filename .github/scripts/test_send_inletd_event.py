import hashlib
import hmac
import unittest
from email import policy
from email.parser import BytesParser

from send_inletd_event import build_message


class BridgeMessageTest(unittest.TestCase):
    def setUp(self):
        self.environment = {
            "GITHUB_RUN_ID": "123",
            "GITHUB_RUN_ATTEMPT": "1",
            "GITHUB_REPOSITORY": "Koe-eigh/inletd",
            "INLETD_GMAIL_SIGNING_KEY": "x" * 32,
            "INLETD_SMTP_USER": "sender@gmail.com",
            "INLETD_GMAIL_TO": "receiver@gmail.com",
        }

    def check_message(self, event_name, event, expected_type):
        self.environment["GITHUB_EVENT_NAME"] = event_name
        message = build_message(event, self.environment)
        parsed = BytesParser(policy=policy.default).parsebytes(message.as_bytes())
        body = parsed.get_payload(decode=True)
        self.assertEqual(parsed.get_content_type(), "text/plain")
        self.assertEqual(
            parsed["X-Inletd-Signature-SHA256"],
            "sha256=" + hmac.new(b"x" * 32, body, hashlib.sha256).hexdigest(),
        )
        import json

        envelope = json.loads(body)
        self.assertEqual(envelope["event_type"], expected_type)
        self.assertEqual(envelope["actor"], event["sender"]["login"])
        return envelope

    def test_pull_request(self):
        envelope = self.check_message(
            "pull_request_target",
            {"action": "opened", "sender": {"login": "alice"}, "pull_request": {"number": 12}},
            "PullRequestEvent",
        )
        self.assertEqual(envelope["payload"]["pull_request"]["number"], 12)

    def test_issue_comment(self):
        envelope = self.check_message(
            "issue_comment",
            {
                "action": "created",
                "sender": {"login": "Koe-eigh"},
                "issue": {"number": 42, "pull_request": {"url": "https://api.github.com/pulls/42"}},
                "comment": {"body": "/inletd review", "user": {"login": "Koe-eigh"}},
            },
            "IssueCommentEvent",
        )
        self.assertEqual(envelope["payload"]["comment"]["body"], "/inletd review")

    def test_short_key_rejected(self):
        self.environment["GITHUB_EVENT_NAME"] = "pull_request_target"
        self.environment["INLETD_GMAIL_SIGNING_KEY"] = "short"
        with self.assertRaises(ValueError):
            build_message({"action": "opened", "sender": {"login": "a"}, "pull_request": {}}, self.environment)


if __name__ == "__main__":
    unittest.main()
