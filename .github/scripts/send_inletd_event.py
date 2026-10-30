"""Send a signed GitHub Actions event to inletd's Gmail source."""

import hashlib
import hmac
import json
import os
from email.message import EmailMessage
from email import policy
from pathlib import Path
import smtplib


def build_message(event, environment):
    event_name = environment["GITHUB_EVENT_NAME"]
    if event_name == "pull_request_target":
        event_type = "PullRequestEvent"
        payload = {"action": event["action"], "pull_request": event["pull_request"]}
    elif event_name == "issue_comment":
        event_type = "IssueCommentEvent"
        payload = {
            "action": event["action"],
            "issue": event["issue"],
            "comment": event["comment"],
        }
    else:
        raise ValueError(f"unsupported event: {event_name}")

    key = environment["INLETD_GMAIL_SIGNING_KEY"].encode("utf-8")
    if len(key) < 32:
        raise ValueError("INLETD_GMAIL_SIGNING_KEY must contain at least 32 bytes")
    envelope = {
        "delivery_id": environment["GITHUB_RUN_ID"] + "-" + environment["GITHUB_RUN_ATTEMPT"],
        "event_type": event_type,
        "repository": environment["GITHUB_REPOSITORY"],
        "actor": event["sender"]["login"],
        "payload": payload,
    }
    body = json.dumps(envelope, ensure_ascii=True, separators=(",", ":")).encode("ascii")
    message = EmailMessage(policy=policy.SMTP.clone(max_line_length=998))
    message["From"] = environment["INLETD_SMTP_USER"]
    message["To"] = environment["INLETD_GMAIL_TO"]
    message["Subject"] = "inletd-event " + environment["GITHUB_REPOSITORY"]
    message["X-Inletd-Signature-SHA256"] = "sha256=" + hmac.new(key, body, hashlib.sha256).hexdigest()
    # Base64 transfer encoding preserves the exact bytes covered by the HMAC.
    message.set_content(body, maintype="text", subtype="plain", cte="base64")
    return message


def main():
    environment = os.environ
    event = json.loads(Path(environment["GITHUB_EVENT_PATH"]).read_text(encoding="utf-8"))
    message = build_message(event, environment)
    with smtplib.SMTP_SSL("smtp.gmail.com", 465, timeout=30) as smtp:
        smtp.login(environment["INLETD_SMTP_USER"], environment["INLETD_SMTP_PASSWORD"])
        smtp.send_message(message)


if __name__ == "__main__":
    main()
