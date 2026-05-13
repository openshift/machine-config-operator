#!/usr/bin/env python3
"""
Standalone script to create Polarion test cases with full custom fields support.
Lives in MCO repo - no external dependencies needed.
"""

import os
import sys
import json
import base64
import requests
import urllib3
from typing import Optional, List, Dict

urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)

POLARION_URL = os.getenv("POLARION_URL", "https://polarion.engineering.redhat.com")
POLARION_TOKEN = os.getenv("POLARION_TOKEN")
PROJECT_ID = os.getenv("POLARION_PROJECT", "OSE")
VERIFY_SSL = os.getenv("POLARION_VERIFY_SSL", "false").lower() == "true"

if not POLARION_TOKEN:
    print(json.dumps({"status": "error", "message": "POLARION_TOKEN environment variable not set"}))
    sys.exit(1)


def get_username_from_token() -> Optional[str]:
    """Extract username from POLARION_TOKEN JWT (sub field)."""
    try:
        payload = POLARION_TOKEN.split(".")[1]
        padding = 4 - len(payload) % 4
        if padding != 4:
            payload += "=" * padding
        decoded = json.loads(base64.urlsafe_b64decode(payload))
        return decoded.get("sub")
    except Exception:
        return None

HEADERS = {
    "Authorization": f"Bearer {POLARION_TOKEN}",
    "Accept": "application/json",
    "Content-Type": "application/json"
}


def create_test_case(
    title: str,
    description: str,
    jira_id: Optional[str] = None,
    version: Optional[str] = None,
    component: str = "Machine Config Operator",
    sub_team: str = "MCO",
    products: str = "OCP",
    status: str = "draft",
    severity: str = "should_have",
    test_type: str = "functional",
    subtype1: str = "-",
    subtype2: str = "-",
    assignee: Optional[str] = None,
    automation_tags: Optional[str] = None
) -> Dict:
    url = f"{POLARION_URL}/polarion/rest/v1/projects/{PROJECT_ID}/workitems"

    attributes = {
        "type": "testcase",
        "title": title,
        "description": {
            "type": "text/html",
            "value": description
        },
        "status": status,
        "severity": severity,
        "casecomponent": component,
        "subteam": sub_team,
    }

    workitem_data = {
        "data": [{
            "type": "workitems",
            "attributes": attributes
        }]
    }

    try:
        response = requests.post(url, headers=HEADERS, json=workitem_data, verify=VERIFY_SSL, timeout=30)

        if response.status_code >= 400:
            return {
                "status": "error",
                "message": f"Failed to create test case: {response.status_code}",
                "error": response.text
            }

        result = response.json()
        test_case_data = result.get("data", [{}])[0]
        test_case_id = test_case_data.get("id", "unknown")

        # PATCH custom fields that don't get set during creation
        patch_fields = {}
        if products:
            patch_fields["caseproducts"] = products
        if jira_id:
            patch_fields["casetrellojira"] = jira_id
        if version:
            patch_fields["caseversion"] = version
        if test_type:
            patch_fields["testtype"] = test_type
        if subtype1:
            patch_fields["caselevel"] = subtype1
        if subtype2:
            patch_fields["subtype2"] = subtype2
        if automation_tags:
            patch_fields["caseautomation"] = automation_tags

        patch_result = None
        if patch_fields:
            patch_result = update_custom_fields(test_case_id, patch_fields)

        assignee_result = None
        resolved_assignee = assignee or get_username_from_token()
        if resolved_assignee:
            assignee_result = set_assignee(test_case_id, resolved_assignee)

        return {
            "status": "success",
            "test_case_id": test_case_id,
            "url": f"{POLARION_URL}/polarion/#/project/{PROJECT_ID}/workitem?id={test_case_id}",
            "title": title,
            "jira_id": jira_id,
            "version": version,
            "products": products,
            "test_type": test_type,
            "patch_result": patch_result,
            "assignee_result": assignee_result
        }

    except Exception as e:
        return {
            "status": "error",
            "message": f"Exception: {str(e)}"
        }


def update_custom_fields(test_case_id: str, fields: Dict) -> Dict:
    """PATCH custom fields on an existing test case."""
    # test_case_id may come as "OSE/OCP-88939", we need just "OCP-88939"
    tc_id = test_case_id.split("/")[-1] if "/" in test_case_id else test_case_id
    url = f"{POLARION_URL}/polarion/rest/v1/projects/{PROJECT_ID}/workitems/{tc_id}"

    patch_data = {
        "data": {
            "type": "workitems",
            "id": f"{PROJECT_ID}/{tc_id}",
            "attributes": fields
        }
    }

    try:
        response = requests.patch(url, headers=HEADERS, json=patch_data, verify=VERIFY_SSL, timeout=30)
        if response.status_code >= 400:
            return {
                "status": "error",
                "message": f"PATCH failed: {response.status_code}",
                "error": response.text,
                "fields_attempted": list(fields.keys())
            }
        return {
            "status": "success",
            "message": f"Updated {len(fields)} custom fields",
            "fields": list(fields.keys())
        }
    except Exception as e:
        return {"status": "error", "message": f"Exception: {str(e)}"}


def set_assignee(test_case_id: str, assignee: str) -> Dict:
    """Set assignee on a test case via PATCH relationships."""
    tc_id = test_case_id.split("/")[-1] if "/" in test_case_id else test_case_id
    url = f"{POLARION_URL}/polarion/rest/v1/projects/{PROJECT_ID}/workitems/{tc_id}"

    patch_data = {
        "data": {
            "type": "workitems",
            "id": f"{PROJECT_ID}/{tc_id}",
            "relationships": {
                "assignee": {
                    "data": [{"type": "users", "id": assignee}]
                }
            }
        }
    }

    try:
        response = requests.patch(url, headers=HEADERS, json=patch_data, verify=VERIFY_SSL, timeout=30)
        if response.status_code >= 400:
            return {
                "status": "error",
                "message": f"Assignee failed: {response.status_code}",
                "error": response.text
            }
        return {"status": "success", "assignee": assignee}
    except Exception as e:
        return {"status": "error", "message": f"Exception: {str(e)}"}


def html_wrap(text: str) -> str:
    """HTML-escape content and wrap in <pre> tags for Polarion rendering."""
    if not text:
        return ""
    import html
    escaped = html.escape(text)
    return f"<pre>{escaped}</pre>"


def add_test_steps(test_case_id: str, test_steps: List[Dict[str, str]]) -> Dict:
    tc_id = test_case_id.split("/")[-1] if "/" in test_case_id else test_case_id
    url = f"{POLARION_URL}/polarion/rest/v1/projects/{PROJECT_ID}/workitems/{tc_id}/teststeps"

    steps_data = []
    for idx, step in enumerate(test_steps):
        step_text = step.get("step", "")
        expected_text = step.get("expectedResult", "")
        steps_data.append({
            "data": {
                "type": "teststeps",
                "attributes": {
                    "index": idx,
                    "values": [
                        {
                            "type": "text/html",
                            "content": step_text,
                            "contentLossy": False
                        },
                        {
                            "type": "text/html",
                            "content": html_wrap(expected_text),
                            "contentLossy": False
                        }
                    ]
                }
            }
        })

    try:
        for step_data in steps_data:
            response = requests.post(url, headers=HEADERS, json=step_data, verify=VERIFY_SSL, timeout=30)
            if response.status_code >= 400:
                return {
                    "status": "error",
                    "message": f"Failed to add test step: {response.status_code}",
                    "error": response.text
                }

        return {
            "status": "success",
            "message": f"Added {len(test_steps)} test steps",
            "test_case_id": tc_id
        }

    except Exception as e:
        return {
            "status": "error",
            "message": f"Exception: {str(e)}"
        }


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("Usage: create_polarion_tc.py '<json-config>'")
        sys.exit(1)

    config = json.loads(sys.argv[1])

    result = create_test_case(**config.get("test_case", {}))

    if result["status"] == "success" and "test_steps" in config:
        steps_result = add_test_steps(
            result["test_case_id"],
            config["test_steps"]
        )
        result["steps"] = steps_result

    print(json.dumps(result, indent=2))
