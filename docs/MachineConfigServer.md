# MachineConfigServer

## Goals

1. Provide Ignition config to new machines joining the cluster.

## Non Goals

## Overview

All the machines joining the cluster must receive configuration from component running inside the cluster. MachineConfigServer provides the Ignition endpoint that all the new machines can point to receive their machine configuration. 

The machine can request specific configuration by pointing Ignition to MachineConfigServer and passing appropriate parameters. For example, to fetch configuration for master the machine can point to `/config/master` whereas, to fetch configuration for worker the machine can point to `/config/worker`.

## Detailed Design

### Endpoint

MachineConfigServer serves Ignition at `/config/<machine-config-pool-name>` endpoint.

* If the server finds the machine config pool requested in the URL, it returns the Ignition config stored in the MachineConfig object referenced at `.status.currentMachineConfig` in the MachineConfigPool object.

* If the server cannot find the machine config pool requested in the URL, the server returns HTTP Status Code 404 with an empty response.

### Ignition config from MachineConfig

MachineConfigServer serves the Ignition config defined in `spec.config` fields of the appropriate MachineConfig object.

It performs the following extra actions on the Ignition config defined in the MachineConfig object before serving it:

* *Ignition file for MachineConfigDaemon*

    MachineConfigDaemon requires a file on disk (node annotations), to seed the `currentConfig` & `desiredConfig` annotations to its node object. The file is JSON object that contains the reference to `MachineConfig` object used to generate the Ignition config for the machine.

* *Ignition file for KubeConfig*

   The new machines that come up, will need a KubeConfig file which will be added as an Ignition file. 

### Running MachineConfigServer

It is recommended that the MachineConfigServer is run as a DaemonSet on all `master` machines with the pods running in host network. So machines can access the Ignition endpoint through load balancer setup for control plane.

### Example requests

1. Worker machine

    Request:

    GET `/config/worker`

    Response:

```json
{
    "ignition": {
        "config": {},
        "security": {
            "tls": {}
        },
        "timeouts": {},
        "version": "2.2.0"
    },
    "networkd": {},
    "passwd": {},
    "storage": {
        "files": [
            {
                "filesystem": "root",
                "path": "/etc/containers/registries.conf",
                "contents": {
                    "source": "data:,%5Bregistries.search%5D%0Aregistries%20%3D%20%5B'registry.access.redhat.com'%2C%20'docker.io'%5D%0A",
                    "verification": {}
                },
                "mode": 420
            },
            {
                "filesystem": "root",
                "path": "/etc/hosts",
                "contents": {
                    "source": "data:,%23%20IPv4%20and%20IPv6%20localhost%20aliases%0A127.0.0.1%09localhost%0A%3A%3A1%09%09localhost%0A%0A%23%20Internal%20registry%20hack%0A10.3.0.25%20docker-registry.default.svc%0A",
                    "verification": {}
                },
                "mode": 420
            },
            {
                "filesystem": "root",
                "path": "/etc/sysconfig/crio-network",
                "contents": {
                    "source": "data:,CRIO_NETWORK_OPTIONS%3D%22--cni-config-dir%3D%2Fetc%2Fkubernetes%2Fcni%2Fnet.d%20--cni-plugin-dir%3D%2Fvar%2Flib%2Fcni%2Fbin%22%0A",
                    "verification": {}
                },
                "mode": 420
            },
            {
                "filesystem": "root",
                "path": "/etc/kubernetes/kubelet.conf",
                "contents": {
                    "source": "data:,kind%3A%20KubeletConfiguration%0AapiVersion%3A%20kubelet.config.k8s.io%2Fv1beta1%0AcgroupDriver%3A%20systemd%0AclusterDNS%3A%0A%20%20-%2010.3.0.10%0AclusterDomain%3A%20cluster.local%0AreadOnlyPort%3A%2010255%0ArotateCertificates%3A%20true%0AruntimeRequestTimeout%3A%2010m%0AserializeImagePulls%3A%20false%0AstaticPodPath%3A%20%2Fetc%2Fkubernetes%2Fmanifests%0A",
                    "verification": {}
                },
                "mode": 420
            },
            {
                "filesystem": "root",
                "path": "/etc/docker/certs.d/docker-registry.default.svc:5000/ca.crt",
                "contents": {
                    "source": "data:,-----BEGIN%20CERTIFICATE-----%0AMIIDCTCCAfGgAwIBAgIBADANBgkqhkiG9w0BAQsFADAmMRIwEAYDVQQLEwlvcGVu%0Ac2hpZnQxEDAOBgNVBAMTB3Jvb3QtY2EwHhcNMTgxMDI0MTc0NjE0WhcNMjgxMDIx%0AMTc0NjE0WjAmMRIwEAYDVQQLEwlvcGVuc2hpZnQxEDAOBgNVBAMTB3Jvb3QtY2Ew%0AggEiMA0GCSqGSIb3DQEBAQUAA4IBDwAwggEKAoIBAQCv8EgOZ%2BvexDJkpmEPuIVv%0ACJtvaJ9TEgpD4d0mN1N%2F2g0GWWP1sNM8lxztyA3mhahNkHLAYRScYjURKlaarXgo%0A0%2BnM2rEkkECn4o7TAetHmBd2%2FFgV3peTucVRIWV801QZMmP9vwCa4yPi2L8Ez37k%0A2RpepeeSVIvHARz7%2BHbMHu5cXauPRazSFko05P2y0VgvdhRzX6zm8DjppLQIHqTH%0AkvsIwEXwsQ8GjUnlqnYhDnI%2F1sTG3SVR3%2FbCobiq5N2JH9wKIfIt89KbNPfE7eH1%0AcTcsS1adPMnAVrviEYk9ukebd3pc9gDFUbxhEJLnMo815sy9O%2FyyrPG%2F3Xfjfn4Z%0AAgMBAAGjQjBAMA4GA1UdDwEB%2FwQEAwICpDAPBgNVHRMBAf8EBTADAQH%2FMB0GA1Ud%0ADgQWBBRRKkS2ZLQotJ2ft4o%2B1xf7hrM17DANBgkqhkiG9w0BAQsFAAOCAQEAj72Y%0AHILMf59%2Bcq%2BkHcwizFJk5dj%2FQaN5Bwe0wT1n%2FjneyV2ISzIC5NVbwcnP2DgZWVOT%0ArxA%2BIBuKH%2FXbjzaDpahgtnK1yqObjSAzsdz7DdstdpriqD0YjBQg23d5idrwyEep%0AF7%2FvdTfWjAZkDrszOCr%2BjWsrsCLUDiBf43u1B9RuuqCsl1bFVAHCK7Gj2cMBXJHd%0AjC4%2BOaZY4TUhmSZIi1nyiie79jMKRFiHtM1P%2BERljT4899faGoGbEHDlYn75HvQA%0AM1Yif0VCtzi%2B6xnKDZ5O3wvxctQTtmb9ayL11d1GT%2FOrM9II0UAtodIjpxBo%2BY7n%0Au4k%2BQSXwlOfqDSixwA%3D%3D%0A-----END%20CERTIFICATE-----%0A",
                    "verification": {}
                },
                "mode": 420
            },
            {
                "filesystem": "root",
                "path": "/etc/kubernetes/ca.crt",
                "contents": {
                    "source": "data:,-----BEGIN%20CERTIFICATE-----%0AMIIDCTCCAfGgAwIBAgIBADANBgkqhkiG9w0BAQsFADAmMRIwEAYDVQQLEwlvcGVu%0Ac2hpZnQxEDAOBgNVBAMTB3Jvb3QtY2EwHhcNMTgxMDI0MTc0NjE0WhcNMjgxMDIx%0AMTc0NjE0WjAmMRIwEAYDVQQLEwlvcGVuc2hpZnQxEDAOBgNVBAMTB3Jvb3QtY2Ew%0AggEiMA0GCSqGSIb3DQEBAQUAA4IBDwAwggEKAoIBAQCv8EgOZ%2BvexDJkpmEPuIVv%0ACJtvaJ9TEgpD4d0mN1N%2F2g0GWWP1sNM8lxztyA3mhahNkHLAYRScYjURKlaarXgo%0A0%2BnM2rEkkECn4o7TAetHmBd2%2FFgV3peTucVRIWV801QZMmP9vwCa4yPi2L8Ez37k%0A2RpepeeSVIvHARz7%2BHbMHu5cXauPRazSFko05P2y0VgvdhRzX6zm8DjppLQIHqTH%0AkvsIwEXwsQ8GjUnlqnYhDnI%2F1sTG3SVR3%2FbCobiq5N2JH9wKIfIt89KbNPfE7eH1%0AcTcsS1adPMnAVrviEYk9ukebd3pc9gDFUbxhEJLnMo815sy9O%2FyyrPG%2F3Xfjfn4Z%0AAgMBAAGjQjBAMA4GA1UdDwEB%2FwQEAwICpDAPBgNVHRMBAf8EBTADAQH%2FMB0GA1Ud%0ADgQWBBRRKkS2ZLQotJ2ft4o%2B1xf7hrM17DANBgkqhkiG9w0BAQsFAAOCAQEAj72Y%0AHILMf59%2Bcq%2BkHcwizFJk5dj%2FQaN5Bwe0wT1n%2FjneyV2ISzIC5NVbwcnP2DgZWVOT%0ArxA%2BIBuKH%2FXbjzaDpahgtnK1yqObjSAzsdz7DdstdpriqD0YjBQg23d5idrwyEep%0AF7%2FvdTfWjAZkDrszOCr%2BjWsrsCLUDiBf43u1B9RuuqCsl1bFVAHCK7Gj2cMBXJHd%0AjC4%2BOaZY4TUhmSZIi1nyiie79jMKRFiHtM1P%2BERljT4899faGoGbEHDlYn75HvQA%0AM1Yif0VCtzi%2B6xnKDZ5O3wvxctQTtmb9ayL11d1GT%2FOrM9II0UAtodIjpxBo%2BY7n%0Au4k%2BQSXwlOfqDSixwA%3D%3D%0A-----END%20CERTIFICATE-----%0A",
                    "verification": {}
                },
                "mode": 420
            },
            {
                "filesystem": "root",
                "path": "/etc/machine-config-daemon/node-annotations.json",
                "contents": {
                    "source": "data:,%7B%22machineconfiguration.openshift.io%2FcurrentConfig%22%3A%223aef043ad5aa416e240b6f207c5cd3b0%22%2C%22machineconfiguration.openshift.io%2FdesiredConfig%22%3A%223aef043ad5aa416e240b6f207c5cd3b0%22%7D",
                    "verification": {}
                },
                "mode": 420
            },
            {
                "filesystem": "root",
                "path": "/etc/kubernetes/kubeconfig",
                "contents": {
                    "source": "data:,clusters%3A%0A-%20cluster%3A%0A%20%20%20%20certificate-authority-data%3A%20LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSURDVENDQWZHZ0F3SUJBZ0lCQURBTkJna3Foa2lHOXcwQkFRc0ZBREFtTVJJd0VBWURWUVFMRXdsdmNHVnUKYzJocFpuUXhFREFPQmdOVkJBTVRCM0p2YjNRdFkyRXdIaGNOTVRneE1ESTBNVGMwTmpFMFdoY05Namd4TURJeApNVGMwTmpFMFdqQW1NUkl3RUFZRFZRUUxFd2x2Y0dWdWMyaHBablF4RURBT0JnTlZCQU1UQjNKdmIzUXRZMkV3CmdnRWlNQTBHQ1NxR1NJYjNEUUVCQVFVQUE0SUJEd0F3Z2dFS0FvSUJBUUN2OEVnT1ordmV4REprcG1FUHVJVnYKQ0p0dmFKOVRFZ3BENGQwbU4xTi8yZzBHV1dQMXNOTThseHp0eUEzbWhhaE5rSExBWVJTY1lqVVJLbGFhclhnbwowK25NMnJFa2tFQ240bzdUQWV0SG1CZDIvRmdWM3BlVHVjVlJJV1Y4MDFRWk1tUDl2d0NhNHlQaTJMOEV6MzdrCjJScGVwZWVTVkl2SEFSejcrSGJNSHU1Y1hhdVBSYXpTRmtvMDVQMnkwVmd2ZGhSelg2em04RGpwcExRSUhxVEgKa3ZzSXdFWHdzUThHalVubHFuWWhEbkkvMXNURzNTVlIzL2JDb2JpcTVOMkpIOXdLSWZJdDg5S2JOUGZFN2VIMQpjVGNzUzFhZFBNbkFWcnZpRVlrOXVrZWJkM3BjOWdERlVieGhFSkxuTW84MTVzeTlPL3l5clBHLzNYZmpmbjRaCkFnTUJBQUdqUWpCQU1BNEdBMVVkRHdFQi93UUVBd0lDcERBUEJnTlZIUk1CQWY4RUJUQURBUUgvTUIwR0ExVWQKRGdRV0JCUlJLa1MyWkxRb3RKMmZ0NG8rMXhmN2hyTTE3REFOQmdrcWhraUc5dzBCQVFzRkFBT0NBUUVBajcyWQpISUxNZjU5K2NxK2tIY3dpekZKazVkai9RYU41QndlMHdUMW4vam5leVYySVN6SUM1TlZid2NuUDJEZ1pXVk9UCnJ4QStJQnVLSC9YYmp6YURwYWhndG5LMXlxT2JqU0F6c2R6N0Rkc3RkcHJpcUQwWWpCUWcyM2Q1aWRyd3lFZXAKRjcvdmRUZldqQVprRHJzek9DcitqV3Nyc0NMVURpQmY0M3UxQjlSdXVxQ3NsMWJGVkFIQ0s3R2oyY01CWEpIZApqQzQrT2FaWTRUVWhtU1pJaTFueWlpZTc5ak1LUkZpSHRNMVArRVJsalQ0ODk5ZmFHb0diRUhEbFluNzVIdlFBCk0xWWlmMFZDdHppKzZ4bktEWjVPM3d2eGN0UVR0bWI5YXlMMTFkMUdUL09yTTlJSTBVQXRvZElqcHhCbytZN24KdTRrK1FTWHdsT2ZxRFNpeHdBPT0KLS0tLS1FTkQgQ0VSVElGSUNBVEUtLS0tLQo%3D%0A%20%20%20%20server%3A%20https%3A%2F%2Fadahiya-0-api.tt.testing%3A6443%0A%20%20name%3A%20adahiya-0%0Acontexts%3A%0A-%20context%3A%0A%20%20%20%20cluster%3A%20adahiya-0%0A%20%20%20%20user%3A%20kubelet%0A%20%20name%3A%20kubelet%0Acurrent-context%3A%20kubelet%0Apreferences%3A%20%7B%7D%0Ausers%3A%0A-%20name%3A%20kubelet%0A%20%20user%3A%0A%20%20%20%20client-certificate-data%3A%20LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSURsRENDQW55Z0F3SUJBZ0lJUGRsdGdsVUN6SVV3RFFZSktvWklodmNOQVFFTEJRQXdKVEVSTUE4R0ExVUUKQ3hNSVltOXZkR3QxWW1VeEVEQU9CZ05WQkFNVEIydDFZbVV0WTJFd0hoY05NVGd4TURJME1UYzBOakUwV2hjTgpNVGd4TURJME1UZ3hOakUzV2pCNE1VSXdLUVlEVlFRS0V5SnplWE4wWlcwNmMyVnlkbWxqWldGalkyOTFiblJ6Ck9tdDFZbVV0YzNsemRHVnRNQlVHQTFVRUNoTU9jM2x6ZEdWdE9tMWhjM1JsY25NeE1qQXdCZ05WQkFNVEtYTjUKYzNSbGJUcHpaWEoyYVdObFlXTmpiM1Z1ZERwcmRXSmxMWE41YzNSbGJUcGtaV1poZFd4ME1JSUJJakFOQmdrcQpoa2lHOXcwQkFRRUZBQU9DQVE4QU1JSUJDZ0tDQVFFQTAyZ2Yyd2FNNDlkUHlhL2N3aDVVZTlkbUhJd2JJNUlDCm5Ud1FLclJHQ0JSRnNJanQwOVdkRXI4NHBtTmpId0pMM1hZZHYrcy9PVkZHR3Vxekp5R2F2N0w2ZmJ1eHhmY1YKb1hWbm04SUM5bHJnMnU2eXB6eFZTQ0RIRW9CMUNqTlZGY094UlNmMk5TMWR1aTkzamU2UGVKWnBxRFZZWUszZAoycmt0Q2NoR0EzMGE0YzNIWHlKWVdxWjljbTY1eCt0b2hCb2g2ZEllWVVMcEVzbmtqSW5QSUh2eFc1L0hZZWExCkR4SnV2eTJxQlMvWmFoczA4d25MOXg1R1Q5emVVYkxsT0dwZlBqOWJmWHUza3phS2liSDZrR2JyanovdWVSZFEKYVZtdTZFMVp6cVJyOFg5YU1YZHVVTXUyRHY3ZEJGdjU0M29RSXQyYmZHR3Y0TjBWclBtV2FRSURBUUFCbzNVdwpjekFPQmdOVkhROEJBZjhFQkFNQ0JhQXdFd1lEVlIwbEJBd3dDZ1lJS3dZQkJRVUhBd0l3REFZRFZSMFRBUUgvCkJBSXdBREFkQmdOVkhRNEVGZ1FVRGxIY2l5cnBHaWYrdU9pcGdvcDhXMTU1UDFrd0h3WURWUjBqQkJnd0ZvQVUKVVNwRXRtUzBLTFNkbjdlS1B0Y1grNGF6TmV3d0RRWUpLb1pJaHZjTkFRRUxCUUFEZ2dFQkFIQUhreUNKWHZJVwo3NC8yQ3NTbUFmTWduOHcwZVBOOU5KK0k4RVc1K1djSzNsY2NWNlZ5K0hrMXRDSWpXQ2RMOS85MjNPTlNzWlBoClZpU1UxRC8rY3FJSXcvbkt0U1lKTDB5elJCaWFZeHVmNXUyV0pVeUZTdGxBVnJReVlISE5tQXZ0NUlONHdlakIKMUFoRjl2YnJJM0sweVIzdjBiK1h6SEZiOWZvNm82YW51YmQ2QmZYMCs4bGxPbW5DZ3VvR1pyUUJzOXQ4NVlWYQpSQWNZellkRU9EOElkVlZNVlFSOWZ0NVhldmR0ekVnR0JPVExTaWhuZ0xIYzcwVmpTbmZMdmEzdDBNMmFhLzZGCmszUDNvVDVDRFRvZnVZeTRkVXRVM1V5Ync1MURYamVxdDdUWkRLYW4xeEJnZzEwdm1YUVdhQ3FiTEpqV0lFSngKNHdrTXduQ25TWDA9Ci0tLS0tRU5EIENFUlRJRklDQVRFLS0tLS0K%0A%20%20%20%20client-key-data%3A%20LS0tLS1CRUdJTiBSU0EgUFJJVkFURSBLRVktLS0tLQpNSUlFb3dJQkFBS0NBUUVBMDJnZjJ3YU00OWRQeWEvY3doNVVlOWRtSEl3Ykk1SUNuVHdRS3JSR0NCUkZzSWp0CjA5V2RFcjg0cG1Oakh3SkwzWFlkditzL09WRkdHdXF6SnlHYXY3TDZmYnV4eGZjVm9YVm5tOElDOWxyZzJ1NnkKcHp4VlNDREhFb0IxQ2pOVkZjT3hSU2YyTlMxZHVpOTNqZTZQZUpacHFEVllZSzNkMnJrdENjaEdBMzBhNGMzSApYeUpZV3FaOWNtNjV4K3RvaEJvaDZkSWVZVUxwRXNua2pJblBJSHZ4VzUvSFllYTFEeEp1dnkycUJTL1phaHMwCjh3bkw5eDVHVDl6ZVViTGxPR3BmUGo5YmZYdTNremFLaWJINmtHYnJqei91ZVJkUWFWbXU2RTFaenFScjhYOWEKTVhkdVVNdTJEdjdkQkZ2NTQzb1FJdDJiZkdHdjROMFZyUG1XYVFJREFRQUJBb0lCQUVqaTFsREtRbHJ2U2RmcwpaUDBjUGQ1d2xnanptUXU3ZEdGSGF2OStKY0wxVWsyWjkvMFg0YzZyMU5rdzNPUzlBdkQ0bnlzaTdTcFN4Z3ZUCnJTNnBuRlBKWGlscFE5SlA3TW84MHhyVldmWWJ3UGhhWVlmYytqNGk1dCtQSUVzREJhdTZTMnpmYVRoT1NzazkKUWtmUjN1OGhWSTRrempLTzN6VmdzSkYxMWdXdlJNeXc4b01UVER0aHhOTXU4NTU3MnQ2bHBlaEtPclFhcWgyKwpqcDlwYmEwNW9yazNCZVhvNk5MazliZVRDUjdibzZSZ2UyeC9Od2dadFlFc0xGb0NndG94VDl5eEYzNDdZTFk1CkpKRlowWFZ3aTJseTJ2b3I3bTJsSXNmVGxlQ3E4bldYUzRER005RHduTktWVVZZbVZmV2R1Ym4zcjlFNlh0VGIKR3Zuckc0RUNnWUVBNFMva1JsNC8xYlJYaGtUcGlKeWNhVHJCZk42cGZlcjZTajI2OUJRVVZ0Zk4rR0dicC94MwowNmJaUFUxK3BYTFBhYjI1aXpyMVAxbTBmaGxRSm55Qk9GdFFuU04zbm94eElCVlE5ZVhjV1FGQ1pqL3VXRzVMCkRtOFl6Mk1BWHE5K0djWXA3MDNLY2ZVNExPODJYWE55SUQrYURncHVMaTFDVFVERUFLS0FGamtDZ1lFQThGV0UKOHNvdm5sSXNITCtaaUJPQ3BYck9rbDlmWW0xRnJ2a0kzTUc5aHZ1V1RnNEorcHZScTFUZjA3U1crcXV0M3VZeQpYOEhvVE1SaE80SUZwZDl0TVdlWWovSnBoQ1FTcU1rcHlnc3hjM28wYzhzNFN2ZzlqOGo1SGFTb1o5UmZ3eWdiCmYzRHJDSDJQcjZleUVPRHErMTZ5cno0NkxiL0RRc1k4T3NUMEFiRUNnWUFndW1vdUJBSzVGNVhrOE4wVU90Yk0Kd0hwZ29LZjNvaEF3ZkJwUTRSNDNwUFBObHJvZHh5YlBQeCt4dGpLaTd6WFFBNEFWQ1VPZHFuYitJTVd5WWtRUgpvY3ZzbXJ3RzhoaDY5ajRuRHZwZ2dUdGFTdzVrRWR1Y3hHN1JyV3pmVmhnNHZNRlpnMi9aOGk3dzhPOXcwNWVSCnNreThuNjExenFRbFFEVjhkaUd4bVFLQmdRRGJ3dWR4OXkyNTBKdmpvZFByV1NQUzIwdi9EbFN6TlFaT0xBeE4KaUo4Y3lmc3ozcVNEVTI1VEE2WXorT05CempDTUxPU05LVXVZdnMzR1UydUV0Snd0Vy9SbVZCem1KdklsQXVWQwppaCtxMzJrTkpSdVJlaE1ZNG9YZzlFckZ2cTNlVDFOdG9qeFlwQy82U0JhTVZvNm9VbnlEd0J3RTcxL0dOR3lvCnRLWUcwUUtCZ0ZwYTY5anBmaW1wSTJJVTFTSmlqZkwxNFVWL09vRXBrUjFNMHU5cVFLZnNsM00xdzBmZ2huRVcKaEFYUUQzSTMxWnBkWkdYT0R6VzVJdUV5aExHaGJIWUgzejZldENJanFpNzdxWTlXbkJ3QjJwQ2Q3YzFuNVRCdwpNcElxV1BDbVQvSEpRUDAyYkhSZGlHSm55aURqZ3FpWnNYVmlrYk1yWjJCNjJqRHkvVVpmCi0tLS0tRU5EIFJTQSBQUklWQVRFIEtFWS0tLS0tCg%3D%3D%0A",
                    "verification": {}
                },
                "mode": 420
            }
        ]
    },
    "systemd": {
        "units": [
            {
                "contents": "[Unit]\nDescription=Kubernetes Kubelet\nWants=rpc-statd.service\n\n[Service]\nExecStartPre=/bin/mkdir --parents /etc/kubernetes/manifests\nEnvironmentFile=-/etc/kubernetes/kubelet-workaround\nEnvironmentFile=-/etc/kubernetes/kubelet-env\n\nExecStart=/usr/bin/hyperkube \\\n    kubelet \\\n      --config=/etc/kubernetes/kubelet.conf \\\n      --bootstrap-kubeconfig=/etc/kubernetes/kubeconfig \\\n      --kubeconfig=/var/lib/kubelet/kubeconfig \\\n      --container-runtime=remote \\\n      --container-runtime-endpoint=/var/run/crio/crio.sock \\\n      --allow-privileged \\\n      --node-labels=node-role.kubernetes.io/worker \\\n      --minimum-container-ttl-duration=6m0s \\\n      --client-ca-file=/etc/kubernetes/ca.crt \\\n      --cloud-provider= \\\n      \\\n      --anonymous-auth=false \\\n\nRestart=always\nRestartSec=10\n\n[Install]\nWantedBy=multi-user.target\n",
                "enabled": true,
                "name": "kubelet.service"
            }
        ]
    }
}
```

2. Master machine with etcd member `etcd-1`

    Request:

    GET `/config/master`

    Response:

```json
{
    "ignition": {
        "config": {},
        "security": {
            "tls": {}
        },
        "timeouts": {},
        "version": "2.2.0"
    },
    "networkd": {},
    "passwd": {},
    "storage": {
        "files": [{
            "filesystem": "root",
            "path": "/etc/containers/registries.conf",
            "contents": {
                "source": "data:,%5Bregistries.search%5D%0Aregistries%20%3D%20%5B'registry.access.redhat.com'%2C%20'docker.io'%5D%0A",
                "verification": {}
            },
            "mode": 420
        }, {
            "filesystem": "root",
            "path": "/etc/hosts",
            "contents": {
                "source": "data:,%23%20IPv4%20and%20IPv6%20localhost%20aliases%0A127.0.0.1%09localhost%0A%3A%3A1%09%09localhost%0A%0A%23%20Internal%20registry%20hack%0A10.3.0.25%20docker-registry.default.svc%0A",
                "verification": {}
            },
            "mode": 420
        }, {
            "filesystem": "root",
            "path": "/etc/sysconfig/crio-network",
            "contents": {
                "source": "data:,CRIO_NETWORK_OPTIONS%3D%22--cni-config-dir%3D%2Fetc%2Fkubernetes%2Fcni%2Fnet.d%20--cni-plugin-dir%3D%2Fvar%2Flib%2Fcni%2Fbin%22%0A",
                "verification": {}
            },
            "mode": 420
        }, {
            "filesystem": "root",
            "path": "/etc/ssl/etcd/ca.crt",
            "contents": {
                "source": "data:,-----BEGIN%20CERTIFICATE-----%0AMIIDKTCCAhGgAwIBAgIIGrsm9RgrvtQwDQYJKoZIhvcNAQELBQAwJjESMBAGA1UE%0ACxMJb3BlbnNoaWZ0MRAwDgYDVQQDEwdyb290LWNhMB4XDTE4MTAyNDE3NDYxNFoX%0ADTI4MTAyMTE3NDYxNFowHjENMAsGA1UECxMEZXRjZDENMAsGA1UEAxMEZXRjZDCC%0AASIwDQYJKoZIhvcNAQEBBQADggEPADCCAQoCggEBALiSeGleHsZVeHZTlgc6qcNw%0AmlceIU5RK6TA3zHp%2ByaopILTn3oVyDozfQqF07H7RZ%2BMAaPFiOfhx6hh27SlvTIe%0AggT3vtVdPtjIsrJppEafdjc37ZDIAV%2BhAqXIAymOxG0IVHrozvbBjNBhr4DqgLpg%0AJyzPRXFdmGHo7Yi9eiR6CTv04cHUYJy7KbbIwT6AFsrW8daO8CmN4yX9pkGsDcvy%0Aordi5ZDgjpkPwhAlqQ7pn52WdELBaCY7Jv1h03inpuYQQpbVnIFxDylR%2FWeuDYz3%0A8%2BacfQ9ZAlVVMfUpqNgYvXvgq%2FEfY20QYYn76%2BZ7wQmNBXevvtU%2FKjUv2UCgoKEC%0AAwEAAaNjMGEwDgYDVR0PAQH%2FBAQDAgKkMA8GA1UdEwEB%2FwQFMAMBAf8wHQYDVR0O%0ABBYEFFEqRLZktCi0nZ%2B3ij7XF%2FuGszXsMB8GA1UdIwQYMBaAFFEqRLZktCi0nZ%2B3%0Aij7XF%2FuGszXsMA0GCSqGSIb3DQEBCwUAA4IBAQArUpdqaJY50u%2BOi39h1vSaliwY%0ABOkQ8xQno2Kkpxoet4FAO7vA2Zav8SEdt4bZdkydwEumNiqpMVrlz%2BpxTn%2BXgpCW%0AchwY2mZ1hlgiElXARPE%2FbJQesYMlogZP%2Bg%2FUcwJj8HJd%2F6d6j9Hsu8amhABjdk3G%0AHCH1h4vZKSz9opVpB1EzI1Y0Ls%2BTLBotpJJSHdRJZnWDNm%2Fjcs8ZnekcPB6RHwxK%0AeoHRt0ChmmdTbLg0FNZXpt4q%2F8zvRdxXmKW98MPhfrqcdCHb4ISjOECb2Mg2B5D6%0AK%2FDK22i8qZ5PgroB71sm4RQd1pup3yF02iZqfuuUzo1kJYawESfn816oounj%0A-----END%20CERTIFICATE-----%0A",
                "verification": {}
            },
            "mode": 420
        }, {
            "filesystem": "root",
            "path": "/etc/ssl/etcd/root-ca.crt",
            "contents": {
                "source": "data:,-----BEGIN%20CERTIFICATE-----%0AMIIDCTCCAfGgAwIBAgIBADANBgkqhkiG9w0BAQsFADAmMRIwEAYDVQQLEwlvcGVu%0Ac2hpZnQxEDAOBgNVBAMTB3Jvb3QtY2EwHhcNMTgxMDI0MTc0NjE0WhcNMjgxMDIx%0AMTc0NjE0WjAmMRIwEAYDVQQLEwlvcGVuc2hpZnQxEDAOBgNVBAMTB3Jvb3QtY2Ew%0AggEiMA0GCSqGSIb3DQEBAQUAA4IBDwAwggEKAoIBAQCv8EgOZ%2BvexDJkpmEPuIVv%0ACJtvaJ9TEgpD4d0mN1N%2F2g0GWWP1sNM8lxztyA3mhahNkHLAYRScYjURKlaarXgo%0A0%2BnM2rEkkECn4o7TAetHmBd2%2FFgV3peTucVRIWV801QZMmP9vwCa4yPi2L8Ez37k%0A2RpepeeSVIvHARz7%2BHbMHu5cXauPRazSFko05P2y0VgvdhRzX6zm8DjppLQIHqTH%0AkvsIwEXwsQ8GjUnlqnYhDnI%2F1sTG3SVR3%2FbCobiq5N2JH9wKIfIt89KbNPfE7eH1%0AcTcsS1adPMnAVrviEYk9ukebd3pc9gDFUbxhEJLnMo815sy9O%2FyyrPG%2F3Xfjfn4Z%0AAgMBAAGjQjBAMA4GA1UdDwEB%2FwQEAwICpDAPBgNVHRMBAf8EBTADAQH%2FMB0GA1Ud%0ADgQWBBRRKkS2ZLQotJ2ft4o%2B1xf7hrM17DANBgkqhkiG9w0BAQsFAAOCAQEAj72Y%0AHILMf59%2Bcq%2BkHcwizFJk5dj%2FQaN5Bwe0wT1n%2FjneyV2ISzIC5NVbwcnP2DgZWVOT%0ArxA%2BIBuKH%2FXbjzaDpahgtnK1yqObjSAzsdz7DdstdpriqD0YjBQg23d5idrwyEep%0AF7%2FvdTfWjAZkDrszOCr%2BjWsrsCLUDiBf43u1B9RuuqCsl1bFVAHCK7Gj2cMBXJHd%0AjC4%2BOaZY4TUhmSZIi1nyiie79jMKRFiHtM1P%2BERljT4899faGoGbEHDlYn75HvQA%0AM1Yif0VCtzi%2B6xnKDZ5O3wvxctQTtmb9ayL11d1GT%2FOrM9II0UAtodIjpxBo%2BY7n%0Au4k%2BQSXwlOfqDSixwA%3D%3D%0A-----END%20CERTIFICATE-----%0A",
                "verification": {}
            },
            "mode": 420
        }, {
            "filesystem": "root",
            "path": "/etc/kubernetes/kubelet.conf",
            "contents": {
                "source": "data:,kind%3A%20KubeletConfiguration%0AapiVersion%3A%20kubelet.config.k8s.io%2Fv1beta1%0AcgroupDriver%3A%20systemd%0AclusterDNS%3A%0A%20%20-%2010.3.0.10%0AclusterDomain%3A%20cluster.local%0AreadOnlyPort%3A%2010255%0AruntimeRequestTimeout%3A%2010m%0AserializeImagePulls%3A%20false%0AstaticPodPath%3A%20%2Fetc%2Fkubernetes%2Fmanifests%0A",
                "verification": {}
            },
            "mode": 420
        }, {
            "filesystem": "root",
            "path": "/etc/modules-load.d/bridge.conf",
            "contents": {
                "source": "data:,br_netfilter%0A",
                "verification": {}
            },
            "mode": 420
        }, {
            "filesystem": "root",
            "path": "/etc/docker/certs.d/docker-registry.default.svc:5000/ca.crt",
            "contents": {
                "source": "data:,-----BEGIN%20CERTIFICATE-----%0AMIIDCTCCAfGgAwIBAgIBADANBgkqhkiG9w0BAQsFADAmMRIwEAYDVQQLEwlvcGVu%0Ac2hpZnQxEDAOBgNVBAMTB3Jvb3QtY2EwHhcNMTgxMDI0MTc0NjE0WhcNMjgxMDIx%0AMTc0NjE0WjAmMRIwEAYDVQQLEwlvcGVuc2hpZnQxEDAOBgNVBAMTB3Jvb3QtY2Ew%0AggEiMA0GCSqGSIb3DQEBAQUAA4IBDwAwggEKAoIBAQCv8EgOZ%2BvexDJkpmEPuIVv%0ACJtvaJ9TEgpD4d0mN1N%2F2g0GWWP1sNM8lxztyA3mhahNkHLAYRScYjURKlaarXgo%0A0%2BnM2rEkkECn4o7TAetHmBd2%2FFgV3peTucVRIWV801QZMmP9vwCa4yPi2L8Ez37k%0A2RpepeeSVIvHARz7%2BHbMHu5cXauPRazSFko05P2y0VgvdhRzX6zm8DjppLQIHqTH%0AkvsIwEXwsQ8GjUnlqnYhDnI%2F1sTG3SVR3%2FbCobiq5N2JH9wKIfIt89KbNPfE7eH1%0AcTcsS1adPMnAVrviEYk9ukebd3pc9gDFUbxhEJLnMo815sy9O%2FyyrPG%2F3Xfjfn4Z%0AAgMBAAGjQjBAMA4GA1UdDwEB%2FwQEAwICpDAPBgNVHRMBAf8EBTADAQH%2FMB0GA1Ud%0ADgQWBBRRKkS2ZLQotJ2ft4o%2B1xf7hrM17DANBgkqhkiG9w0BAQsFAAOCAQEAj72Y%0AHILMf59%2Bcq%2BkHcwizFJk5dj%2FQaN5Bwe0wT1n%2FjneyV2ISzIC5NVbwcnP2DgZWVOT%0ArxA%2BIBuKH%2FXbjzaDpahgtnK1yqObjSAzsdz7DdstdpriqD0YjBQg23d5idrwyEep%0AF7%2FvdTfWjAZkDrszOCr%2BjWsrsCLUDiBf43u1B9RuuqCsl1bFVAHCK7Gj2cMBXJHd%0AjC4%2BOaZY4TUhmSZIi1nyiie79jMKRFiHtM1P%2BERljT4899faGoGbEHDlYn75HvQA%0AM1Yif0VCtzi%2B6xnKDZ5O3wvxctQTtmb9ayL11d1GT%2FOrM9II0UAtodIjpxBo%2BY7n%0Au4k%2BQSXwlOfqDSixwA%3D%3D%0A-----END%20CERTIFICATE-----%0A",
                "verification": {}
            },
            "mode": 420
        }, {
            "filesystem": "root",
            "path": "/etc/kubernetes/ca.crt",
            "contents": {
                "source": "data:,-----BEGIN%20CERTIFICATE-----%0AMIIDCTCCAfGgAwIBAgIBADANBgkqhkiG9w0BAQsFADAmMRIwEAYDVQQLEwlvcGVu%0Ac2hpZnQxEDAOBgNVBAMTB3Jvb3QtY2EwHhcNMTgxMDI0MTc0NjE0WhcNMjgxMDIx%0AMTc0NjE0WjAmMRIwEAYDVQQLEwlvcGVuc2hpZnQxEDAOBgNVBAMTB3Jvb3QtY2Ew%0AggEiMA0GCSqGSIb3DQEBAQUAA4IBDwAwggEKAoIBAQCv8EgOZ%2BvexDJkpmEPuIVv%0ACJtvaJ9TEgpD4d0mN1N%2F2g0GWWP1sNM8lxztyA3mhahNkHLAYRScYjURKlaarXgo%0A0%2BnM2rEkkECn4o7TAetHmBd2%2FFgV3peTucVRIWV801QZMmP9vwCa4yPi2L8Ez37k%0A2RpepeeSVIvHARz7%2BHbMHu5cXauPRazSFko05P2y0VgvdhRzX6zm8DjppLQIHqTH%0AkvsIwEXwsQ8GjUnlqnYhDnI%2F1sTG3SVR3%2FbCobiq5N2JH9wKIfIt89KbNPfE7eH1%0AcTcsS1adPMnAVrviEYk9ukebd3pc9gDFUbxhEJLnMo815sy9O%2FyyrPG%2F3Xfjfn4Z%0AAgMBAAGjQjBAMA4GA1UdDwEB%2FwQEAwICpDAPBgNVHRMBAf8EBTADAQH%2FMB0GA1Ud%0ADgQWBBRRKkS2ZLQotJ2ft4o%2B1xf7hrM17DANBgkqhkiG9w0BAQsFAAOCAQEAj72Y%0AHILMf59%2Bcq%2BkHcwizFJk5dj%2FQaN5Bwe0wT1n%2FjneyV2ISzIC5NVbwcnP2DgZWVOT%0ArxA%2BIBuKH%2FXbjzaDpahgtnK1yqObjSAzsdz7DdstdpriqD0YjBQg23d5idrwyEep%0AF7%2FvdTfWjAZkDrszOCr%2BjWsrsCLUDiBf43u1B9RuuqCsl1bFVAHCK7Gj2cMBXJHd%0AjC4%2BOaZY4TUhmSZIi1nyiie79jMKRFiHtM1P%2BERljT4899faGoGbEHDlYn75HvQA%0AM1Yif0VCtzi%2B6xnKDZ5O3wvxctQTtmb9ayL11d1GT%2FOrM9II0UAtodIjpxBo%2BY7n%0Au4k%2BQSXwlOfqDSixwA%3D%3D%0A-----END%20CERTIFICATE-----%0A",
                "verification": {}
            },
            "mode": 420
        }, {
            "filesystem": "root",
            "path": "/etc/sysctl.d/bridge.conf",
            "contents": {
                "source": "data:,net.bridge.bridge-nf-call-ip6tables%20%3D%201%0Anet.bridge.bridge-nf-call-iptables%20%3D%201%0Anet.bridge.bridge-nf-call-arptables%20%3D%201%0A",
                "verification": {}
            },
            "mode": 420
        }, {
            "filesystem": "root",
            "path": "/etc/machine-config-daemon/node-annotations.json",
            "contents": {
                "source": "data:,%7B%22machineconfiguration.openshift.io%2FcurrentConfig%22%3A%22be2ec8753b61b4ffcb0e1aca92d7936a%22%2C%22machineconfiguration.openshift.io%2FdesiredConfig%22%3A%22be2ec8753b61b4ffcb0e1aca92d7936a%22%7D",
                "verification": {}
            },
            "mode": 420
        }, {
            "filesystem": "root",
            "path": "/etc/kubernetes/kubeconfig",
            "contents": {
                "source": "data:,clusters%3A%0A-%20cluster%3A%0A%20%20%20%20certificate-authority-data%3A%20LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSURDVENDQWZHZ0F3SUJBZ0lCQURBTkJna3Foa2lHOXcwQkFRc0ZBREFtTVJJd0VBWURWUVFMRXdsdmNHVnUKYzJocFpuUXhFREFPQmdOVkJBTVRCM0p2YjNRdFkyRXdIaGNOTVRneE1ESTBNVGMwTmpFMFdoY05Namd4TURJeApNVGMwTmpFMFdqQW1NUkl3RUFZRFZRUUxFd2x2Y0dWdWMyaHBablF4RURBT0JnTlZCQU1UQjNKdmIzUXRZMkV3CmdnRWlNQTBHQ1NxR1NJYjNEUUVCQVFVQUE0SUJEd0F3Z2dFS0FvSUJBUUN2OEVnT1ordmV4REprcG1FUHVJVnYKQ0p0dmFKOVRFZ3BENGQwbU4xTi8yZzBHV1dQMXNOTThseHp0eUEzbWhhaE5rSExBWVJTY1lqVVJLbGFhclhnbwowK25NMnJFa2tFQ240bzdUQWV0SG1CZDIvRmdWM3BlVHVjVlJJV1Y4MDFRWk1tUDl2d0NhNHlQaTJMOEV6MzdrCjJScGVwZWVTVkl2SEFSejcrSGJNSHU1Y1hhdVBSYXpTRmtvMDVQMnkwVmd2ZGhSelg2em04RGpwcExRSUhxVEgKa3ZzSXdFWHdzUThHalVubHFuWWhEbkkvMXNURzNTVlIzL2JDb2JpcTVOMkpIOXdLSWZJdDg5S2JOUGZFN2VIMQpjVGNzUzFhZFBNbkFWcnZpRVlrOXVrZWJkM3BjOWdERlVieGhFSkxuTW84MTVzeTlPL3l5clBHLzNYZmpmbjRaCkFnTUJBQUdqUWpCQU1BNEdBMVVkRHdFQi93UUVBd0lDcERBUEJnTlZIUk1CQWY4RUJUQURBUUgvTUIwR0ExVWQKRGdRV0JCUlJLa1MyWkxRb3RKMmZ0NG8rMXhmN2hyTTE3REFOQmdrcWhraUc5dzBCQVFzRkFBT0NBUUVBajcyWQpISUxNZjU5K2NxK2tIY3dpekZKazVkai9RYU41QndlMHdUMW4vam5leVYySVN6SUM1TlZid2NuUDJEZ1pXVk9UCnJ4QStJQnVLSC9YYmp6YURwYWhndG5LMXlxT2JqU0F6c2R6N0Rkc3RkcHJpcUQwWWpCUWcyM2Q1aWRyd3lFZXAKRjcvdmRUZldqQVprRHJzek9DcitqV3Nyc0NMVURpQmY0M3UxQjlSdXVxQ3NsMWJGVkFIQ0s3R2oyY01CWEpIZApqQzQrT2FaWTRUVWhtU1pJaTFueWlpZTc5ak1LUkZpSHRNMVArRVJsalQ0ODk5ZmFHb0diRUhEbFluNzVIdlFBCk0xWWlmMFZDdHppKzZ4bktEWjVPM3d2eGN0UVR0bWI5YXlMMTFkMUdUL09yTTlJSTBVQXRvZElqcHhCbytZN24KdTRrK1FTWHdsT2ZxRFNpeHdBPT0KLS0tLS1FTkQgQ0VSVElGSUNBVEUtLS0tLQo%3D%0A%20%20%20%20server%3A%20https%3A%2F%2Fadahiya-0-api.tt.testing%3A6443%0A%20%20name%3A%20adahiya-0%0Acontexts%3A%0A-%20context%3A%0A%20%20%20%20cluster%3A%20adahiya-0%0A%20%20%20%20user%3A%20kubelet%0A%20%20name%3A%20kubelet%0Acurrent-context%3A%20kubelet%0Apreferences%3A%20%7B%7D%0Ausers%3A%0A-%20name%3A%20kubelet%0A%20%20user%3A%0A%20%20%20%20client-certificate-data%3A%20LS0tLS1CRUdJTiBDRVJUSUZJQ0FURS0tLS0tCk1JSURsRENDQW55Z0F3SUJBZ0lJUGRsdGdsVUN6SVV3RFFZSktvWklodmNOQVFFTEJRQXdKVEVSTUE4R0ExVUUKQ3hNSVltOXZkR3QxWW1VeEVEQU9CZ05WQkFNVEIydDFZbVV0WTJFd0hoY05NVGd4TURJME1UYzBOakUwV2hjTgpNVGd4TURJME1UZ3hOakUzV2pCNE1VSXdLUVlEVlFRS0V5SnplWE4wWlcwNmMyVnlkbWxqWldGalkyOTFiblJ6Ck9tdDFZbVV0YzNsemRHVnRNQlVHQTFVRUNoTU9jM2x6ZEdWdE9tMWhjM1JsY25NeE1qQXdCZ05WQkFNVEtYTjUKYzNSbGJUcHpaWEoyYVdObFlXTmpiM1Z1ZERwcmRXSmxMWE41YzNSbGJUcGtaV1poZFd4ME1JSUJJakFOQmdrcQpoa2lHOXcwQkFRRUZBQU9DQVE4QU1JSUJDZ0tDQVFFQTAyZ2Yyd2FNNDlkUHlhL2N3aDVVZTlkbUhJd2JJNUlDCm5Ud1FLclJHQ0JSRnNJanQwOVdkRXI4NHBtTmpId0pMM1hZZHYrcy9PVkZHR3Vxekp5R2F2N0w2ZmJ1eHhmY1YKb1hWbm04SUM5bHJnMnU2eXB6eFZTQ0RIRW9CMUNqTlZGY094UlNmMk5TMWR1aTkzamU2UGVKWnBxRFZZWUszZAoycmt0Q2NoR0EzMGE0YzNIWHlKWVdxWjljbTY1eCt0b2hCb2g2ZEllWVVMcEVzbmtqSW5QSUh2eFc1L0hZZWExCkR4SnV2eTJxQlMvWmFoczA4d25MOXg1R1Q5emVVYkxsT0dwZlBqOWJmWHUza3phS2liSDZrR2JyanovdWVSZFEKYVZtdTZFMVp6cVJyOFg5YU1YZHVVTXUyRHY3ZEJGdjU0M29RSXQyYmZHR3Y0TjBWclBtV2FRSURBUUFCbzNVdwpjekFPQmdOVkhROEJBZjhFQkFNQ0JhQXdFd1lEVlIwbEJBd3dDZ1lJS3dZQkJRVUhBd0l3REFZRFZSMFRBUUgvCkJBSXdBREFkQmdOVkhRNEVGZ1FVRGxIY2l5cnBHaWYrdU9pcGdvcDhXMTU1UDFrd0h3WURWUjBqQkJnd0ZvQVUKVVNwRXRtUzBLTFNkbjdlS1B0Y1grNGF6TmV3d0RRWUpLb1pJaHZjTkFRRUxCUUFEZ2dFQkFIQUhreUNKWHZJVwo3NC8yQ3NTbUFmTWduOHcwZVBOOU5KK0k4RVc1K1djSzNsY2NWNlZ5K0hrMXRDSWpXQ2RMOS85MjNPTlNzWlBoClZpU1UxRC8rY3FJSXcvbkt0U1lKTDB5elJCaWFZeHVmNXUyV0pVeUZTdGxBVnJReVlISE5tQXZ0NUlONHdlakIKMUFoRjl2YnJJM0sweVIzdjBiK1h6SEZiOWZvNm82YW51YmQ2QmZYMCs4bGxPbW5DZ3VvR1pyUUJzOXQ4NVlWYQpSQWNZellkRU9EOElkVlZNVlFSOWZ0NVhldmR0ekVnR0JPVExTaWhuZ0xIYzcwVmpTbmZMdmEzdDBNMmFhLzZGCmszUDNvVDVDRFRvZnVZeTRkVXRVM1V5Ync1MURYamVxdDdUWkRLYW4xeEJnZzEwdm1YUVdhQ3FiTEpqV0lFSngKNHdrTXduQ25TWDA9Ci0tLS0tRU5EIENFUlRJRklDQVRFLS0tLS0K%0A%20%20%20%20client-key-data%3A%20LS0tLS1CRUdJTiBSU0EgUFJJVkFURSBLRVktLS0tLQpNSUlFb3dJQkFBS0NBUUVBMDJnZjJ3YU00OWRQeWEvY3doNVVlOWRtSEl3Ykk1SUNuVHdRS3JSR0NCUkZzSWp0CjA5V2RFcjg0cG1Oakh3SkwzWFlkditzL09WRkdHdXF6SnlHYXY3TDZmYnV4eGZjVm9YVm5tOElDOWxyZzJ1NnkKcHp4VlNDREhFb0IxQ2pOVkZjT3hSU2YyTlMxZHVpOTNqZTZQZUpacHFEVllZSzNkMnJrdENjaEdBMzBhNGMzSApYeUpZV3FaOWNtNjV4K3RvaEJvaDZkSWVZVUxwRXNua2pJblBJSHZ4VzUvSFllYTFEeEp1dnkycUJTL1phaHMwCjh3bkw5eDVHVDl6ZVViTGxPR3BmUGo5YmZYdTNremFLaWJINmtHYnJqei91ZVJkUWFWbXU2RTFaenFScjhYOWEKTVhkdVVNdTJEdjdkQkZ2NTQzb1FJdDJiZkdHdjROMFZyUG1XYVFJREFRQUJBb0lCQUVqaTFsREtRbHJ2U2RmcwpaUDBjUGQ1d2xnanptUXU3ZEdGSGF2OStKY0wxVWsyWjkvMFg0YzZyMU5rdzNPUzlBdkQ0bnlzaTdTcFN4Z3ZUCnJTNnBuRlBKWGlscFE5SlA3TW84MHhyVldmWWJ3UGhhWVlmYytqNGk1dCtQSUVzREJhdTZTMnpmYVRoT1NzazkKUWtmUjN1OGhWSTRrempLTzN6VmdzSkYxMWdXdlJNeXc4b01UVER0aHhOTXU4NTU3MnQ2bHBlaEtPclFhcWgyKwpqcDlwYmEwNW9yazNCZVhvNk5MazliZVRDUjdibzZSZ2UyeC9Od2dadFlFc0xGb0NndG94VDl5eEYzNDdZTFk1CkpKRlowWFZ3aTJseTJ2b3I3bTJsSXNmVGxlQ3E4bldYUzRER005RHduTktWVVZZbVZmV2R1Ym4zcjlFNlh0VGIKR3Zuckc0RUNnWUVBNFMva1JsNC8xYlJYaGtUcGlKeWNhVHJCZk42cGZlcjZTajI2OUJRVVZ0Zk4rR0dicC94MwowNmJaUFUxK3BYTFBhYjI1aXpyMVAxbTBmaGxRSm55Qk9GdFFuU04zbm94eElCVlE5ZVhjV1FGQ1pqL3VXRzVMCkRtOFl6Mk1BWHE5K0djWXA3MDNLY2ZVNExPODJYWE55SUQrYURncHVMaTFDVFVERUFLS0FGamtDZ1lFQThGV0UKOHNvdm5sSXNITCtaaUJPQ3BYck9rbDlmWW0xRnJ2a0kzTUc5aHZ1V1RnNEorcHZScTFUZjA3U1crcXV0M3VZeQpYOEhvVE1SaE80SUZwZDl0TVdlWWovSnBoQ1FTcU1rcHlnc3hjM28wYzhzNFN2ZzlqOGo1SGFTb1o5UmZ3eWdiCmYzRHJDSDJQcjZleUVPRHErMTZ5cno0NkxiL0RRc1k4T3NUMEFiRUNnWUFndW1vdUJBSzVGNVhrOE4wVU90Yk0Kd0hwZ29LZjNvaEF3ZkJwUTRSNDNwUFBObHJvZHh5YlBQeCt4dGpLaTd6WFFBNEFWQ1VPZHFuYitJTVd5WWtRUgpvY3ZzbXJ3RzhoaDY5ajRuRHZwZ2dUdGFTdzVrRWR1Y3hHN1JyV3pmVmhnNHZNRlpnMi9aOGk3dzhPOXcwNWVSCnNreThuNjExenFRbFFEVjhkaUd4bVFLQmdRRGJ3dWR4OXkyNTBKdmpvZFByV1NQUzIwdi9EbFN6TlFaT0xBeE4KaUo4Y3lmc3ozcVNEVTI1VEE2WXorT05CempDTUxPU05LVXVZdnMzR1UydUV0Snd0Vy9SbVZCem1KdklsQXVWQwppaCtxMzJrTkpSdVJlaE1ZNG9YZzlFckZ2cTNlVDFOdG9qeFlwQy82U0JhTVZvNm9VbnlEd0J3RTcxL0dOR3lvCnRLWUcwUUtCZ0ZwYTY5anBmaW1wSTJJVTFTSmlqZkwxNFVWL09vRXBrUjFNMHU5cVFLZnNsM00xdzBmZ2huRVcKaEFYUUQzSTMxWnBkWkdYT0R6VzVJdUV5aExHaGJIWUgzejZldENJanFpNzdxWTlXbkJ3QjJwQ2Q3YzFuNVRCdwpNcElxV1BDbVQvSEpRUDAyYkhSZGlHSm55aURqZ3FpWnNYVmlrYk1yWjJCNjJqRHkvVVpmCi0tLS0tRU5EIFJTQSBQUklWQVRFIEtFWS0tLS0tCg%3D%3D%0A",
                "verification": {}
            },
            "mode": 420
        }]
    },
    "systemd": {
        "units": [{
            "contents": "[Unit]\nDescription=etcd (System Application Container)\nDocumentation=https://github.com/coreos/etcd\nAfter=network-online.target\nWants=network-online.target\nRequires=setup-etcd-environment.service\n\n[Service]\nRestart=on-failure\nRestartSec=10s\nTimeoutStartSec=0\nLimitNOFILE=40000\n\n## FIXME(abhinav): these images should be replacable by release image.\nEnvironment=\"SIGNER_IMAGE=quay.io/coreos/kube-client-agent:678cc8e6841e2121ebfdb6e2db568fce290b67d6\"\nEnvironment=\"ETCD_IMAGE=quay.io/coreos/etcd:v3.2.14\"\nEnvironmentFile=/etc/etcd.env/etcd-environment\n\nExecStartPre=/bin/sh -c \" \\\n  [ -e /etc/ssl/etcd/system:etcd-server:${ETCD_DNS_NAME}.crt -a \\\n    -e /etc/ssl/etcd/system:etcd-server:${ETCD_DNS_NAME}.key ] || \\\n  /bin/podman \\\n    run \\\n      --rm \\\n      --volume /etc/ssl/etcd:/etc/ssl/etcd:rw,z \\\n      --network host \\\n      '${SIGNER_IMAGE}' \\\n        request \\\n          --orgname=system:etcd-servers \\\n          --cacrt=/etc/ssl/etcd/root-ca.crt \\\n          --assetsdir=/etc/ssl/etcd \\\n          --address=https://adahiya-0-api.tt.testing:6443 \\\n          --dnsnames=localhost,etcd.kube-system.svc,etcd.kube-system.svc.cluster.local,${ETCD_DNS_NAME} \\\n          --commonname=system:etcd-server:${ETCD_DNS_NAME} \\\n          --ipaddrs=${ETCD_IPV4_ADDRESS},127.0.0.1 \\\n\"\nExecStartPre=/bin/chown etcd:etcd /etc/ssl/etcd/system:etcd-server:${ETCD_DNS_NAME}.crt\nExecStartPre=/bin/chown etcd:etcd /etc/ssl/etcd/system:etcd-server:${ETCD_DNS_NAME}.key\n\nExecStartPre=/bin/sh -c \" \\\n  [ -e /etc/ssl/etcd/system:etcd-peer:${ETCD_DNS_NAME}.crt -a \\\n    -e /etc/ssl/etcd/system:etcd-peer:${ETCD_DNS_NAME}.key ] || \\\n  /bin/podman \\\n    run \\\n      --rm \\\n      --volume /etc/ssl/etcd:/etc/ssl/etcd:rw,z \\\n      --network host \\\n      '${SIGNER_IMAGE}' \\\n        request \\\n          --orgname=system:etcd-peers \\\n          --cacrt=/etc/ssl/etcd/root-ca.crt \\\n          --assetsdir=/etc/ssl/etcd \\\n          --address=https://adahiya-0-api.tt.testing:6443 \\\n          --dnsnames=${ETCD_DNS_NAME},tt.testing \\\n          --commonname=system:etcd-peer:${ETCD_DNS_NAME} \\\n          --ipaddrs=${ETCD_IPV4_ADDRESS} \\\n\"\nExecStartPre=/bin/chown etcd:etcd /etc/ssl/etcd/system:etcd-peer:${ETCD_DNS_NAME}.crt\nExecStartPre=/bin/chown etcd:etcd /etc/ssl/etcd/system:etcd-peer:${ETCD_DNS_NAME}.key\n\nExecStartPre=-/bin/podman rm etcd-member\nExecStartPre=/usr/bin/mkdir --parents /var/lib/etcd\nExecStartPre=/usr/bin/mkdir --parents /run/etcd\nExecStartPre=/usr/bin/chown etcd /var/lib/etcd\nExecStartPre=/usr/bin/chown etcd /run/etcd\n\nExecStart= /usr/bin/bash -c \" \\\n    /bin/podman \\\n      run \\\n        --rm \\\n        --name etcd-member \\\n        --volume /run/systemd/system:/run/systemd/system:ro,z \\\n        --volume /etc/ssl/certs:/etc/ssl/certs:ro,z \\\n        --volume /etc/ssl/etcd:/etc/ssl/etcd:ro,z \\\n        --volume /var/lib/etcd:/var/lib/etcd:rw,z \\\n        --volume /etc/ssl/certs:/etc/ssl/certs:ro,z \\\n        --env 'ETCD_NAME=%m' \\\n        --env ETCD_DATA_DIR=/var/lib/etcd \\\n        --network host \\\n        --user=$(id --user etcd) \\\n      '${ETCD_IMAGE}' \\\n        /usr/local/bin/etcd \\\n          --name ${ETCD_DNS_NAME} \\\n          --discovery-srv tt.testing \\\n          --initial-advertise-peer-urls=https://${ETCD_IPV4_ADDRESS}:2380 \\\n          --cert-file=/etc/ssl/etcd/system:etcd-server:${ETCD_DNS_NAME}.crt \\\n          --key-file=/etc/ssl/etcd/system:etcd-server:${ETCD_DNS_NAME}.key \\\n          --trusted-ca-file=/etc/ssl/etcd/ca.crt \\\n          --client-cert-auth=true \\\n          --peer-cert-file=/etc/ssl/etcd/system:etcd-peer:${ETCD_DNS_NAME}.crt \\\n          --peer-key-file=/etc/ssl/etcd/system:etcd-peer:${ETCD_DNS_NAME}.key \\\n          --peer-trusted-ca-file=/etc/ssl/etcd/ca.crt \\\n          --peer-client-cert-auth=true \\\n          --advertise-client-urls=https://${ETCD_IPV4_ADDRESS}:2379 \\\n          --listen-client-urls=https://0.0.0.0:2379 \\\n          --listen-peer-urls=https://0.0.0.0:2380 \\\n    \"\n\n[Install]\nWantedBy=multi-user.target\n",
            "enabled": true,
            "name": "etcd-member.service"
        }, {
            "contents": "[Unit]\nDescription=Kubernetes Kubelet\nWants=rpc-statd.service\n\n[Service]\nExecStartPre=/bin/mkdir --parents /etc/kubernetes/manifests\nEnvironmentFile=-/etc/kubernetes/kubelet-workaround\nEnvironmentFile=-/etc/kubernetes/kubelet-env\n\nExecStart=/usr/bin/hyperkube \\\n    kubelet \\\n      --config=/etc/kubernetes/kubelet.conf \\\n      --bootstrap-kubeconfig=/etc/kubernetes/kubeconfig \\\n      --rotate-certificates \\\n      --kubeconfig=/var/lib/kubelet/kubeconfig \\\n      --container-runtime=remote \\\n      --container-runtime-endpoint=/var/run/crio/crio.sock \\\n      --allow-privileged \\\n      --node-labels=node-role.kubernetes.io/master \\\n      --minimum-container-ttl-duration=6m0s \\\n      --client-ca-file=/etc/kubernetes/ca.crt \\\n      --cloud-provider= \\\n      \\\n      --anonymous-auth=false \\\n      --register-with-taints=node-role.kubernetes.io/master=:NoSchedule \\\n\nRestart=always\nRestartSec=10\n\n[Install]\nWantedBy=multi-user.target\n",
            "enabled": true,
            "name": "kubelet.service"
        }, {
            "contents": "[Unit]\nDescription=Setup Etcd Environment  \nRequires=network-online.target  \nAfter=network-online.target\n\n[Service]\nRemainAfterExit=yes  \nType=oneshot\n\n## FIXME(abhinav): switch this to official image.\nEnvironment=\"IMAGE=docker.io/abhinavdahiya/origin-setup-etcd-environment\"\n\nExecStartPre=/usr/bin/mkdir --parents /etc/etcd.env\nExecStart=/bin/podman \\\n  run \\\n    --net host \\\n    --rm \\\n    --volume /etc/etcd.env:/etc/etcd.env:z \\\n    ${IMAGE} \\\n      --discovery-srv=tt.testing \\\n      --output-file=/etc/etcd.env/etcd-environment \\\n      --v=4 \\\n\n[Install]\nWantedBy=multi-user.target\n",
            "enabled": true,
            "name": "setup-etcd-environment.service"
        }]
    }
}
```
