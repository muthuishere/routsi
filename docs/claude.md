---
title: Use Claude on AWS with routsi
---

# Use Claude on AWS with routsi

Routsi calls Amazon Bedrock's `Converse` API directly with AWS SigV4. It uses native
Claude tool use and explicit Bedrock prompt-cache checkpoints. It does not launch
Claude Code, extract a Claude token, or emulate tool calls in a prompt.

## Prerequisites

1. Enable a Claude model or cross-region inference profile in Amazon Bedrock.
2. Give the AWS identity `bedrock:InvokeModel` permission for that resource.
3. Log in as the same OS user that runs Routsi:

```sh
aws login
aws sts get-caller-identity
```

For an IAM Identity Center/SSO profile, use:

```sh
aws sso login --profile my-profile
aws sts get-caller-identity --profile my-profile
```

Routsi uses the AWS SDK default credential chain, including environment credentials,
shared AWS config, `aws login`/SSO sessions, `credential_process`, web identity,
container credentials, and instance roles. Credentials stay in AWS's stores and are
refreshed by the SDK; never copy them into `models.yaml`.

## Configure the built-in adapter

The adapter is compiled into Routsi and self-registers as `claude-bedrock`. There is
no adapter install command, script path, handler registration, or CLI dependency.

```yaml
models:
  - name: claude-aws
    type: subscription
    provider: claude-bedrock
    upstream_model: us.anthropic.claude-sonnet-4-5-20250929-v1:0
    aws_region: us-east-1
    aws_profile: default
```

`upstream_model` may be a Bedrock model ID, inference-profile ID, or ARN. Omit
`aws_profile` to use the normal default chain. Omit `aws_region` when the selected AWS
profile or `AWS_REGION` already supplies it.

Call it through Routsi's OpenAI-compatible endpoint:

```sh
curl http://127.0.0.1:11080/v1/chat/completions \
  -H 'content-type: application/json' \
  -d '{"model":"claude-aws","messages":[{"role":"user","content":"Explain this repository."}]}'
```

## Native tool calls and prompt caching

OpenAI function definitions are mapped to Bedrock `ToolSpecification` values. Claude's
`toolUse` response is returned as an OpenAI `tool_calls` response, and the following
OpenAI `role: tool` message becomes a Bedrock `toolResult`. Routsi never executes the
tool itself.

Stable tool definitions are sorted and followed by a Bedrock cache point. System
instructions also receive an explicit cache point. Cache creation still depends on
the selected model's Bedrock caching support and minimum-token rules.

## Live E2E

Live tests are opt-in because Bedrock model IDs, access, region, and charges belong to
the AWS account:

```sh
ROUTSI_BEDROCK_TEST_MODEL='YOUR_ENABLED_MODEL_OR_PROFILE_ID' \
  go test ./internal/server -run '^TestLiveClaudeBedrock' -count=1 -v
```

Optionally set `ROUTSI_BEDROCK_TEST_REGION` and `ROUTSI_BEDROCK_TEST_PROFILE`. The
tests skip under `-short`, when the model variable is absent, or when the AWS login is
missing/expired. They cover exact text and a native schema-backed tool call.

## Troubleshooting

- Expired login: run `aws login`, or `aws sso login --profile NAME`.
- Missing region: set `aws_region`, `AWS_REGION`, or the profile's region.
- Access denied: verify both Bedrock model access and IAM `bedrock:InvokeModel`.
- Validation error: verify the model/inference-profile ID exists in the configured
  region and supports Converse/tool use/cache points.
