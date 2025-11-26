package internalreleaseimage

var iriRegistryServiceTemplate string = `name: iri-registry.service
enabled: true
contents: |
  [Unit]
  Description=InternalReleaseImage Registry
  Wants=network.target

  [Service]
  Environment=PODMAN_SYSTEMD_UNIT=%n
  ExecStartPre=/bin/rm -f %t/%n.ctr-id
  ExecStartPre=/usr/bin/mkdir -p /var/lib/myregistry
  ExecStart=podman run --net host --cidfile=%t/%n.ctr-id --log-driver=journald --replace --name=iri-registry -v /var/lib/iri-registry:/var/lib/registry:ro -e REGISTRY_HTTP_ADDR=0.0.0.0:22625 registry.ci.openshift.org/ocp/4.21-2025-11-25-150640@sha256:b94d119a8fc931fbd80c4e256455d170a495cf20c633894cd44c6077a883c798
  ExecStop=/usr/bin/podman stop --ignore --cidfile=%t/%n.ctr-id
  ExecStopPost=/usr/bin/podman rm -f --ignore --cidfile=%t/%n.ctr-id

  Restart=on-failure
  RestartSec=10
  TimeoutStartSec=9000
  TimeoutStopSec=300
  
  [Install]
  WantedBy=multi-user.target`
