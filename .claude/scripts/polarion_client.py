"""
Polarion REST API Client
Adapted from ~/VSCode/polarion-mcp-server/polarion_client.py
"""

import os
import requests
import base64
from typing import Optional, Dict, Any, List


class PolarionClient:
    """Client for Polarion REST API operations with SOAP fallback"""

    def __init__(
        self,
        url: str,
        token: str,
        verify_ssl: bool = True,
        username: Optional[str] = None,
        password: Optional[str] = None
    ):
        self.url = url
        self.token = token
        self.verify_ssl = verify_ssl
        self.base_url = f"{url}/polarion/rest/v1"
        self.username = username
        self.password = password
        self.soap_url = f"{url}/polarion/ws/services/TestManagementWebService"

    def _make_request(
        self,
        method: str,
        endpoint: str,
        data: Optional[Dict] = None,
        params: Optional[Dict] = None
    ) -> Dict[str, Any]:
        """Make authenticated request to Polarion REST API"""

        url = f"{self.base_url}/{endpoint}"
        headers = {
            "Authorization": f"Bearer {self.token}",
            "Accept": "application/json",
            "Content-Type": "application/json"
        }

        try:
            response = requests.request(
                method=method,
                url=url,
                headers=headers,
                json=data,
                params=params,
                timeout=30,
                verify=self.verify_ssl
            )

            if response.status_code == 401:
                return {
                    "error": "Authentication failed. Check POLARION_TOKEN.",
                    "status": 401
                }

            if response.status_code >= 400:
                try:
                    error_data = response.json()
                    return {
                        "error": error_data.get("errors", [{}])[0].get("detail", "Unknown error"),
                        "status": response.status_code,
                        "response": error_data
                    }
                except (ValueError, KeyError, IndexError):
                    return {
                        "error": response.text,
                        "status": response.status_code
                    }

            return response.json() if response.content else {"success": True}

        except Exception as e:
            return {"error": str(e), "status": 0}

    def get_test_case(
        self,
        test_case_id: str,
        project_id: str,
        include_test_steps: bool = True
    ) -> Dict[str, Any]:
        """Get test case details with test steps"""

        params = {
            "fields[workitems]": "title,description,status,type"
        }
        if include_test_steps:
            params["include"] = "testSteps"
            params["fields[teststeps]"] = "values"

        result = self._make_request(
            "GET",
            f"projects/{project_id}/workitems/{test_case_id}",
            params=params
        )

        if "error" in result:
            return {
                "status": "failed",
                "error": result["error"]
            }

        return {
            "status": "success",
            "data": result
        }

    def get_workitem_custom_fields(
        self,
        test_case_id: str,
        project_id: str
    ) -> Dict[str, Any]:
        """Get work item with all custom fields"""

        # Get full work item to access custom fields
        result = self._make_request(
            "GET",
            f"projects/{project_id}/workitems/{test_case_id}",
            params={}
        )

        if "error" in result:
            return {
                "status": "failed",
                "error": result["error"]
            }

        return {
            "status": "success",
            "data": result
        }

    def _soap_get_test_steps(
        self,
        test_case_id: str,
        project_id: str
    ) -> Dict[str, Any]:
        """Get test steps using SOAP API (requires username/password)"""

        if not self.username or not self.password:
            return {
                "status": "failed",
                "error": "SOAP API requires username and password. Set POLARION_USERNAME and POLARION_PASSWORD in .env"
            }

        # Build SOAP request to get test steps
        work_item_uri = f"subterra:data-service:objects:/default/{project_id}${{WorkItem}}{test_case_id}"

        soap_request = f"""<?xml version="1.0" encoding="UTF-8"?>
<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/"
                  xmlns:tes="http://ws.polarion.com/TestManagementWebService">
   <soapenv:Header/>
   <soapenv:Body>
      <tes:getTestSteps>
         <tes:workItemURI>{work_item_uri}</tes:workItemURI>
      </tes:getTestSteps>
   </soapenv:Body>
</soapenv:Envelope>"""

        # Make SOAP request with Basic Auth
        auth = base64.b64encode(f"{self.username}:{self.password}".encode()).decode()
        headers = {
            "Content-Type": "text/xml; charset=utf-8",
            "SOAPAction": "",
            "Authorization": f"Basic {auth}"
        }

        try:
            response = requests.post(
                self.soap_url,
                data=soap_request,
                headers=headers,
                verify=self.verify_ssl,
                timeout=30
            )

            if response.status_code == 200:
                # Parse SOAP response to extract test steps
                import xml.etree.ElementTree as ET

                root = ET.fromstring(response.text)
                steps = []

                # Find all step elements in the response
                for step_elem in root.findall(".//{http://ws.polarion.com/TestManagementWebService}steps"):
                    values = step_elem.findall(".//{http://ws.polarion.com/TestManagementWebService}values")

                    step_text = ""
                    expected_text = ""

                    if len(values) >= 1:
                        content = values[0].find(".//{http://ws.polarion.com/TestManagementWebService}content")
                        if content is not None and content.text:
                            step_text = content.text

                    if len(values) >= 2:
                        content = values[1].find(".//{http://ws.polarion.com/TestManagementWebService}content")
                        if content is not None and content.text:
                            expected_text = content.text

                    steps.append({
                        'step': step_text,
                        'expected_result': expected_text
                    })

                return {
                    "status": "success",
                    "steps": steps
                }
            else:
                return {
                    "status": "failed",
                    "error": f"SOAP request failed: {response.status_code}"
                }

        except Exception as e:
            return {
                "status": "failed",
                "error": f"SOAP API error: {str(e)}"
            }

    def _soap_set_test_steps(
        self,
        test_case_id: str,
        test_steps: List[Dict[str, str]],
        project_id: str
    ) -> Dict[str, Any]:
        """Set test steps using SOAP API (requires username/password)"""

        if not self.username or not self.password:
            return {
                "status": "failed",
                "error": "SOAP API requires username and password. Set POLARION_USERNAME and POLARION_PASSWORD in .env"
            }

        # Build SOAP request
        work_item_uri = f"subterra:data-service:objects:/default/{project_id}${{WorkItem}}{test_case_id}"

        steps_xml = ""
        for idx, step in enumerate(test_steps):
            steps_xml += f"""
            <steps>
                <index>{idx}</index>
                <values>
                    <Text>
                        <type>text/html</type>
                        <content>{step.get('step', '').replace('&', '&amp;').replace('<', '&lt;').replace('>', '&gt;').replace(chr(10), '<br/>')}</content>
                        <contentLossy>false</contentLossy>
                    </Text>
                    <Text>
                        <type>text/html</type>
                        <content>{step.get('expected_result', '').replace('&', '&amp;').replace('<', '&lt;').replace('>', '&gt;').replace(chr(10), '<br/>')}</content>
                        <contentLossy>false</contentLossy>
                    </Text>
                </values>
            </steps>"""

        soap_request = f"""<?xml version="1.0" encoding="UTF-8"?>
<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/"
                  xmlns:tes="http://ws.polarion.com/TestManagementWebService">
   <soapenv:Header/>
   <soapenv:Body>
      <tes:setTestSteps>
         <tes:workItemURI>{work_item_uri}</tes:workItemURI>
         <tes:testSteps>
            <tes:keys>step</tes:keys>
            <tes:keys>expectedResult</tes:keys>
            {steps_xml}
         </tes:testSteps>
      </tes:setTestSteps>
   </soapenv:Body>
</soapenv:Envelope>"""

        # Make SOAP request with Basic Auth
        auth = base64.b64encode(f"{self.username}:{self.password}".encode()).decode()
        headers = {
            "Content-Type": "text/xml; charset=utf-8",
            "SOAPAction": "",
            "Authorization": f"Basic {auth}"
        }

        try:
            response = requests.post(
                self.soap_url,
                data=soap_request,
                headers=headers,
                verify=self.verify_ssl,
                timeout=30
            )

            if response.status_code == 200:
                if "<Fault" in response.text or ":Fault" in response.text:
                    return {
                        "status": "failed",
                        "error": f"SOAP fault while updating test steps: {response.text[:500]}"
                    }
                return {
                    "status": "success",
                    "message": f"Updated {len(test_steps)} test steps via SOAP API",
                    "test_case_id": test_case_id,
                    "steps_added": len(test_steps),
                    "method": "SOAP"
                }
            else:
                return {
                    "status": "failed",
                    "error": f"SOAP request failed: {response.status_code} - {response.text}"
                }

        except Exception as e:
            return {
                "status": "failed",
                "error": f"SOAP API error: {str(e)}"
            }

    def add_test_steps(
        self,
        test_case_id: str,
        test_steps: List[Dict[str, str]],
        project_id: str,
        force_soap: bool = False
    ) -> Dict[str, Any]:
        """
        Add/update test steps.

        REST API Limitation:
        - Can only POST steps to NEW test cases (blank slate)
        - For EXISTING test cases with steps: MUST use SOAP API

        Strategies:
        1. If force_soap=True: Use SOAP API directly
        2. Otherwise: Try REST API first, fall back to SOAP if needed
        """

        # Force SOAP if requested
        if force_soap:
            return self._soap_set_test_steps(test_case_id, test_steps, project_id)

        try:
            # Check if test steps already exist
            check_url = f"projects/{project_id}/workitems/{test_case_id}/relationships/testSteps"
            existing = self._make_request("GET", check_url)

            has_existing_steps = (
                "error" not in existing and
                len(existing.get("data", [])) > 0
            )

            # If steps exist, must use SOAP API
            if has_existing_steps:
                return self._soap_set_test_steps(test_case_id, test_steps, project_id)

            # No existing steps - try REST API
            steps_data = []
            for step in test_steps:
                step_obj = {
                    "type": "teststeps",
                    "attributes": {
                        "keys": ["step", "expectedResult"],
                        "values": [
                            {"type": "text/html", "value": step.get("step", "").replace("\n", "<br/>")},
                            {"type": "text/html", "value": step.get("expected_result", "").replace("\n", "<br/>")}
                        ]
                    }
                }
                steps_data.append(step_obj)

            # POST test steps via REST API
            result = self._make_request(
                "POST",
                f"projects/{project_id}/workitems/{test_case_id}/teststeps",
                data={"data": steps_data}
            )

            if "error" in result:
                # REST failed, try SOAP fallback
                return self._soap_set_test_steps(test_case_id, test_steps, project_id)

            created_steps = result.get("data", [])

            return {
                "status": "success",
                "message": f"Added {len(created_steps)} test steps via REST API",
                "test_case_id": test_case_id,
                "steps_added": len(created_steps),
                "method": "REST"
            }

        except Exception as e:
            return {
                "status": "failed",
                "error": str(e)
            }
