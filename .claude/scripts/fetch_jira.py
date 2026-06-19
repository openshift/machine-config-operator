#!/usr/bin/env python3
"""
Fetch Jira issue from redhat.atlassian.net.

Usage:
    python3 fetch_jira.py MCO-2221
    python3 fetch_jira.py OCPBUGS-74223

Output:
    - /tmp/test-specs/jira/<issue-key>.json (draft format)
    - JSON to stdout (summary, description, fix versions, acceptance criteria)

No MCP required - uses direct Atlassian REST API.
"""

import json
import os
import sys
import re
from typing import Dict, Any, Optional
import requests


def load_env():
    """Load .env file from repo root"""
    script_dir = os.path.dirname(os.path.abspath(__file__))
    repo_root = os.path.dirname(os.path.dirname(script_dir))
    env_file = os.path.join(repo_root, '.env')

    env_vars = {}
    if os.path.exists(env_file):
        with open(env_file) as f:
            for line in f:
                line = line.strip()
                if line and not line.startswith('#') and '=' in line:
                    key, value = line.split('=', 1)
                    env_vars[key] = value.strip('"').strip("'")

    return env_vars


def fetch_jira_issue(issue_key: str, jira_url: str, jira_token: Optional[str] = None, jira_email: Optional[str] = None) -> Dict[str, Any]:
    """Fetch Jira issue from Atlassian REST API"""

    # Build URL
    api_url = f"{jira_url}/rest/api/3/issue/{issue_key}"

    # Headers
    headers = {
        "Accept": "application/json",
        "Content-Type": "application/json"
    }

    # Auth: Try Bearer token first, fall back to Basic auth (email:token)
    if jira_token:
        if jira_email:
            # Basic Auth with email:token (Atlassian Cloud requires this)
            import base64
            credentials = base64.b64encode(f"{jira_email}:{jira_token}".encode()).decode()
            headers["Authorization"] = f"Basic {credentials}"
        else:
            # Try Bearer token (may not work for all Atlassian instances)
            headers["Authorization"] = f"Bearer {jira_token}"

    try:
        response = requests.get(api_url, headers=headers, timeout=30)
        response.raise_for_status()

        return response.json()

    except requests.HTTPError as e:
        if e.response.status_code == 401:
            raise Exception("Authentication failed. Check JIRA_TOKEN in .env") from e
        elif e.response.status_code == 404:
            raise Exception(f"Jira issue not found: {issue_key}") from e
        else:
            raise Exception(f"Jira API error: HTTP {e.response.status_code}") from e
    except requests.RequestException as e:
        raise Exception(f"Failed to fetch Jira issue: {type(e).__name__}") from e


def parse_jira_to_draft(issue_data: Dict, issue_key: str) -> Dict[str, Any]:
    """Parse Jira issue data to Polarion draft format"""

    fields = issue_data.get('fields', {})

    # Summary
    summary = fields.get('summary', '')

    # Description (may be in ADF or plain text)
    description_obj = fields.get('description', {})
    description = ''

    if isinstance(description_obj, dict):
        # ADF format
        if description_obj.get('type') == 'doc':
            # Extract text from ADF content
            description = extract_text_from_adf(description_obj)
        elif 'content' in description_obj:
            description = str(description_obj.get('content', ''))
    elif isinstance(description_obj, str):
        description = description_obj

    # Fix Versions
    fix_versions = fields.get('fixVersions', [])
    version = ''
    if fix_versions:
        # Take first version, extract version number (e.g., "4.18.0" -> "4.18")
        version_name = fix_versions[0].get('name', '')
        # Extract X.Y from X.Y.Z
        version_match = re.match(r'(\d+\.\d+)', version_name)
        if version_match:
            version = version_match.group(1)
        else:
            version = version_name

    # Acceptance Criteria (custom field - may vary by project)
    # Common field names: customfield_12311140, acceptanceCriteria
    acceptance_criteria = ''

    # Try common acceptance criteria field names
    for field_name in ['customfield_12311140', 'customfield_12315140', 'acceptanceCriteria']:
        if field_name in fields:
            ac_value = fields[field_name]
            if isinstance(ac_value, str):
                acceptance_criteria = ac_value
            elif isinstance(ac_value, dict):
                # ADF format
                acceptance_criteria = extract_text_from_adf(ac_value)
            break

    # Issue Type
    issue_type = fields.get('issuetype', {}).get('name', '')

    # Labels
    labels = fields.get('labels', [])

    # Build draft
    # Title format: [MCO][ISSUE-KEY] Summary
    title = f"[MCO][{issue_key}] {summary}"

    draft = {
        'title': title,
        'description': description or summary,
        'component': 'Machine Config Operator',
        'sub_team': 'MCO',
        'products': 'OCP',
        'test_type': 'Functional',  # Default, can be overridden
        'version': version,
        'trello_jira': issue_key,
        'test_steps': [],  # To be filled from PR or manually
        'metadata': {
            'source_jira': issue_key,
            'jira_type': issue_type,
            'labels': labels,
            'acceptance_criteria': acceptance_criteria
        }
    }

    return draft


def extract_text_from_adf(adf: Dict) -> str:
    """Extract plain text from Atlassian Document Format (ADF)"""
    if not isinstance(adf, dict):
        return str(adf)

    text_parts = []

    def extract_recursive(node):
        if isinstance(node, dict):
            # Text node
            if node.get('type') == 'text':
                text_parts.append(node.get('text', ''))

            # Paragraph, heading, etc.
            if 'content' in node:
                for child in node['content']:
                    extract_recursive(child)

        elif isinstance(node, list):
            for item in node:
                extract_recursive(item)

    extract_recursive(adf)

    return '\n'.join(text_parts).strip()


def main():
    import argparse

    parser = argparse.ArgumentParser(
        description='Fetch Jira issue and convert to Polarion draft',
        epilog="""
Examples:
  python3 fetch_jira.py MCO-2221
  python3 fetch_jira.py OCPBUGS-74223
  python3 fetch_jira.py MCO-2221 --output /tmp/custom.json

Requires:
  JIRA_URL in .env (default: https://redhat.atlassian.net)
  JIRA_EMAIL in .env (your Red Hat email)
  JIRA_TOKEN in .env (Atlassian API token)
        """
    )

    parser.add_argument('issue_key', help='Jira issue key (e.g., MCO-2221, OCPBUGS-74223)')
    parser.add_argument('--output', help='Output file path (default: /tmp/test-specs/jira/<issue-key>.json)')

    args = parser.parse_args()

    if not re.fullmatch(r'[A-Z][A-Z0-9]+-\d+', args.issue_key):
        print(f"Error: invalid Jira issue key format: {args.issue_key}", file=sys.stderr)
        sys.exit(1)

    # Load environment
    env = load_env()
    jira_url = env.get('JIRA_URL', 'https://redhat.atlassian.net')
    jira_token = env.get('JIRA_TOKEN')
    jira_email = env.get('JIRA_EMAIL')

    if not jira_token:
        print("Warning: JIRA_TOKEN not found in .env (may fail with auth error)", file=sys.stderr)
        print("Generate token at: https://id.atlassian.com/manage-profile/security/api-tokens", file=sys.stderr)
        print()

    if jira_token and not jira_email:
        print("Warning: JIRA_EMAIL not found in .env (Basic auth may not work)", file=sys.stderr)
        print("Set JIRA_EMAIL=you@redhat.com in .env for Atlassian Cloud authentication", file=sys.stderr)
        print()

    try:
        # Fetch Jira issue
        print(f"Fetching Jira issue: {args.issue_key} from {jira_url}", file=sys.stderr)
        issue_data = fetch_jira_issue(args.issue_key, jira_url, jira_token, jira_email)

        # Parse to draft
        draft = parse_jira_to_draft(issue_data, args.issue_key)

        # Output file
        if args.output:
            output_file = args.output
        else:
            output_dir = '/tmp/test-specs/jira'
            os.makedirs(output_dir, exist_ok=True)
            output_file = os.path.join(output_dir, f"{args.issue_key}.json")

        # Write draft
        with open(output_file, 'w') as f:
            json.dump(draft, f, indent=2)

        print(f"\n✓ Draft saved to: {output_file}", file=sys.stderr)
        print(f"  Title: {draft['title']}", file=sys.stderr)
        print(f"  Version: {draft.get('version', 'NOT FOUND')}", file=sys.stderr)
        print(f"  Jira Type: {draft['metadata'].get('jira_type', 'Unknown')}", file=sys.stderr)

        if draft['metadata'].get('acceptance_criteria'):
            print(f"  Acceptance Criteria: Found", file=sys.stderr)

        # Output JSON to stdout
        print(json.dumps(draft, indent=2))

    except Exception as e:
        print(f"Error: {e}", file=sys.stderr)
        import traceback
        traceback.print_exc()
        sys.exit(1)


if __name__ == '__main__':
    main()
