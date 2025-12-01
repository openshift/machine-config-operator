package internalreleaseimage

var iriRegistryServiceTemplate = `name: iri-registry.service
enabled: true
contents: |
  [Unit]
  Description=InternalReleaseImage Registry
  Wants=network.target

  [Service]
  Environment=PODMAN_SYSTEMD_UNIT=%n
  ExecStartPre=/bin/rm -f %t/%n.ctr-id
  ExecStartPre=/usr/bin/mkdir -p /var/lib/iri-registry
  ExecStart=podman run --net host --cidfile=%t/%n.ctr-id --log-driver=journald --replace --name=iri-registry -v /var/lib/iri-registry:/var/lib/registry:ro -e REGISTRY_HTTP_ADDR=0.0.0.0:22625 -u 0 --entrypoint=/usr/bin/distribution {{ .DockerRegistryImage }} serve /etc/registry/config.yaml
  ExecStop=/usr/bin/podman stop --ignore --cidfile=%t/%n.ctr-id
  ExecStopPost=/usr/bin/podman rm -f --ignore --cidfile=%t/%n.ctr-id

  Restart=on-failure
  RestartSec=10
  TimeoutStartSec=9000
  TimeoutStopSec=300
  
  [Install]
  WantedBy=multi-user.target`
