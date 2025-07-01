module github.com/openshift/machine-config-operator

go 1.23.0

toolchain go1.23.5

require (
	github.com/BurntSushi/toml v1.4.1-0.20240526193622-a339e1f7089c
	github.com/InVisionApp/go-health v2.1.0+incompatible
	github.com/apparentlymart/go-cidr v1.0.0
	github.com/ashcrow/osrelease v0.0.0-20180626175927-9b292693c55c
	github.com/clarketm/json v1.17.1
	github.com/containers/common v0.62.1
	github.com/containers/image/v5 v5.34.1
	github.com/containers/kubensmnt v1.2.0
	github.com/containers/storage v1.57.1
	github.com/coreos/fcct v0.5.0
	github.com/coreos/go-semver v0.3.1
	github.com/coreos/ign-converter v0.0.0-20241125185625-2f773079ca81
	github.com/coreos/ignition v0.35.0
	github.com/coreos/ignition/v2 v2.20.0
	github.com/coreos/rpmostree-client-go v0.0.0-20230914135003-fae0786302f7
	github.com/coreos/stream-metadata-go v0.4.3
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc
	github.com/distribution/reference v0.6.0
	github.com/fsnotify/fsnotify v1.8.0
	github.com/ghodss/yaml v1.0.1-0.20190212211648-25d852aebe32
	github.com/golangci/golangci-lint v1.62.0
	github.com/google/go-cmp v0.6.0
	github.com/google/renameio v0.1.0
	github.com/google/uuid v1.6.0
	github.com/imdario/mergo v0.3.16
	github.com/onsi/ginkgo/v2 v2.22.2
	github.com/onsi/gomega v1.36.2
	github.com/opencontainers/go-digest v1.0.0
	github.com/openshift-eng/openshift-tests-extension v0.0.0-20250522124649-4ffcd156ec7c
	github.com/openshift/api v0.0.0-20250620092249-a8cbc218cd2c
	github.com/openshift/client-go v0.0.0-20250623095455-7b2007868c76
	github.com/openshift/library-go v0.0.0-20250129210218-fe56c2cf5d70
	github.com/openshift/runtime-utils v0.0.0-20230921210328-7bdb5b9c177b
	github.com/prometheus/client_golang v1.20.5
	github.com/rs/zerolog v1.34.0
	github.com/spf13/cobra v1.8.1
	github.com/spf13/pflag v1.0.6
	github.com/stretchr/testify v1.10.0
	github.com/tidwall/sjson v1.2.5
	github.com/vincent-petithory/dataurl v1.0.0
	golang.org/x/net v0.37.0
	golang.org/x/time v0.9.0
	google.golang.org/grpc v1.69.4
	k8s.io/api v0.32.3
	k8s.io/apiextensions-apiserver v0.32.1
	k8s.io/apimachinery v0.32.3
	k8s.io/client-go v0.32.1
	k8s.io/code-generator v0.32.1
	k8s.io/component-base v0.32.1
	k8s.io/cri-api v0.32.1
	k8s.io/kubectl v0.32.1
	k8s.io/kubelet v0.32.1
	k8s.io/kubernetes v0.0.0-00010101000000-000000000000
	k8s.io/utils v0.0.0-20241210054802-24370beab758
	sigs.k8s.io/controller-runtime v0.20.1
	sigs.k8s.io/controller-runtime/tools/setup-envtest v0.0.0-20240522175850-2e9781e9fc60
)

require (
	4d63.com/gocheckcompilerdirectives v1.2.1 // indirect
	cel.dev/expr v0.18.0 // indirect
	github.com/4meepo/tagalign v1.3.4 // indirect
	github.com/Abirdcfly/dupword v0.1.3 // indirect
	github.com/Antonboom/testifylint v1.5.2 // indirect
	github.com/Azure/go-ansiterm v0.0.0-20230124172434-306776ec8161 // indirect
	github.com/Crocmagnon/fatcontext v0.5.3 // indirect
	github.com/GaijinEntertainment/go-exhaustruct/v3 v3.3.0 // indirect
	github.com/JeffAshton/win_pdh v0.0.0-20161109143554-76bb4ee9f0ab // indirect
	github.com/MakeNowJust/heredoc v1.0.0 // indirect
	github.com/Masterminds/semver/v3 v3.3.0 // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/NYTimes/gziphandler v1.1.1 // indirect
	github.com/OpenPeeDeeP/depguard/v2 v2.2.0 // indirect
	github.com/alecthomas/go-check-sumtype v0.3.1 // indirect
	github.com/alexkohler/nakedret/v2 v2.0.5 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.0 // indirect
	github.com/asaskevich/govalidator v0.0.0-20230301143203-a9d515a09cc2 // indirect
	github.com/aws/aws-sdk-go v1.55.5 // indirect
	github.com/bombsimon/wsl/v4 v4.5.0 // indirect
	github.com/butuzov/mirror v1.3.0 // indirect
	github.com/catenacyber/perfsprint v0.7.1 // indirect
	github.com/ccojocar/zxcvbn-go v1.0.2 // indirect
	github.com/cenkalti/backoff/v4 v4.3.0 // indirect
	github.com/chai2010/gettext-go v1.0.2 // indirect
	github.com/ckaznocha/intrange v0.3.0 // indirect
	github.com/container-storage-interface/spec v1.9.0 // indirect
	github.com/containerd/containerd/api v1.7.19 // indirect
	github.com/containerd/errdefs v0.3.0 // indirect
	github.com/containerd/log v0.1.0 // indirect
	github.com/containerd/ttrpc v1.2.5 // indirect
	github.com/cyberphone/json-canonicalization v0.0.0-20231217050601-ba74d44ecf5f // indirect
	github.com/cyphar/filepath-securejoin v0.3.6 // indirect
	github.com/euank/go-kmsg-parser v2.0.0+incompatible // indirect
	github.com/exponent-io/jsonpath v0.0.0-20210407135951-1de76d718b3f // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/fxamacker/cbor/v2 v2.7.0 // indirect
	github.com/ghostiam/protogetter v0.3.8 // indirect
	github.com/go-errors/errors v1.4.2 // indirect
	github.com/go-jose/go-jose/v4 v4.0.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-logr/zapr v1.3.0 // indirect
	github.com/go-openapi/analysis v0.23.0 // indirect
	github.com/go-openapi/errors v0.22.0 // indirect
	github.com/go-openapi/loads v0.22.0 // indirect
	github.com/go-openapi/runtime v0.28.0 // indirect
	github.com/go-openapi/spec v0.21.0 // indirect
	github.com/go-openapi/strfmt v0.23.0 // indirect
	github.com/go-openapi/validate v0.24.0 // indirect
	github.com/go-task/slim-sprig/v3 v3.0.0 // indirect
	github.com/go-viper/mapstructure/v2 v2.2.1 // indirect
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/golangci/go-printf-func-name v0.1.0 // indirect
	github.com/golangci/modinfo v0.3.4 // indirect
	github.com/golangci/plugin-module-register v0.1.1 // indirect
	github.com/google/btree v1.1.3 // indirect
	github.com/google/cadvisor v0.51.0 // indirect
	github.com/google/cel-go v0.22.0 // indirect
	github.com/google/gnostic-models v0.6.9 // indirect
	github.com/google/pprof v0.0.0-20241210010833-40e02aabc2ad // indirect
	github.com/google/shlex v0.0.0-20191202100458-e7afc7fbc510 // indirect
	github.com/gorilla/websocket v1.5.0 // indirect
	github.com/gregjones/httpcache v0.0.0-20190611155906-901d90724c79 // indirect
	github.com/grpc-ecosystem/go-grpc-prometheus v1.2.1-0.20210315223345-82c243799c99 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.22.0 // indirect
	github.com/jjti/go-spancheck v0.6.4 // indirect
	github.com/karamaru-alpha/copyloopvar v1.1.0 // indirect
	github.com/karrick/godirwalk v1.17.0 // indirect
	github.com/kkHAIKE/contextcheck v1.1.5 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/lasiar/canonicalheader v1.1.2 // indirect
	github.com/ldez/grignotin v0.7.0 // indirect
	github.com/liggitt/tabwriter v0.0.0-20181228230101-89fcab3d43de // indirect
	github.com/macabu/inamedparam v0.1.3 // indirect
	github.com/maratori/testableexamples v1.0.0 // indirect
	github.com/mistifyio/go-zfs v2.1.2-0.20190413222219-f784269be439+incompatible // indirect
	github.com/mitchellh/go-wordwrap v1.0.1 // indirect
	github.com/moby/spdystream v0.5.0 // indirect
	github.com/moby/sys/capability v0.4.0 // indirect
	github.com/moby/sys/user v0.3.0 // indirect
	github.com/moby/sys/userns v0.1.0 // indirect
	github.com/moby/term v0.5.0 // indirect
	github.com/monochromegane/go-gitignore v0.0.0-20200626010858-205db1a8cc00 // indirect
	github.com/mxk/go-flowrate v0.0.0-20140419014527-cca7078d478f // indirect
	github.com/nunnatsa/ginkgolinter v0.18.0 // indirect
	github.com/oklog/ulid v1.3.1 // indirect
	github.com/onsi/ginkgo v1.16.5 // indirect
	github.com/opencontainers/runc v1.2.4 // indirect
	github.com/opencontainers/selinux v1.11.1 // indirect
	github.com/peterbourgon/diskv v2.0.1+incompatible // indirect
	github.com/quasilyte/go-ruleguard/dsl v0.3.22 // indirect
	github.com/raeperd/recvcheck v0.1.2 // indirect
	github.com/robfig/cron v1.2.0 // indirect
	github.com/rogpeppe/go-internal v1.13.1 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	github.com/sagikazarmark/locafero v0.4.0 // indirect
	github.com/sagikazarmark/slog-shim v0.1.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v5 v5.3.1 // indirect
	github.com/secure-systems-lab/go-securesystemslib v0.9.0 // indirect
	github.com/sergi/go-diff v1.3.2-0.20230802210424-5b0b94c5c0d3 // indirect
	github.com/sigstore/fulcio v1.6.4 // indirect
	github.com/sigstore/rekor v1.3.8 // indirect
	github.com/sourcegraph/conc v0.3.0 // indirect
	github.com/stoewer/go-strcase v1.3.0 // indirect
	github.com/tidwall/gjson v1.14.2 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	github.com/timonwong/loggercheck v0.10.1 // indirect
	github.com/uudashr/iface v1.2.0 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	github.com/xen0n/gosmopolitan v1.2.2 // indirect
	github.com/xlab/treeprint v1.2.0 // indirect
	github.com/ykadowak/zerologlint v0.1.5 // indirect
	go-simpler.org/musttag v0.13.0 // indirect
	go-simpler.org/sloglint v0.7.2 // indirect
	go.etcd.io/etcd/api/v3 v3.5.16 // indirect
	go.etcd.io/etcd/client/pkg/v3 v3.5.16 // indirect
	go.etcd.io/etcd/client/v3 v3.5.16 // indirect
	go.mongodb.org/mongo-driver v1.14.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.54.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.54.0 // indirect
	go.opentelemetry.io/otel v1.31.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.28.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.27.0 // indirect
	go.opentelemetry.io/otel/metric v1.31.0 // indirect
	go.opentelemetry.io/otel/sdk v1.31.0 // indirect
	go.opentelemetry.io/otel/trace v1.31.0 // indirect
	go.opentelemetry.io/proto/otlp v1.3.1 // indirect
	go.uber.org/atomic v1.9.0 // indirect
	go.uber.org/automaxprocs v1.6.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20241209162323-e6fa225c2576 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250102185135-69823020774d // indirect
	gopkg.in/DATA-DOG/go-sqlmock.v1 v1.3.0 // indirect
	gopkg.in/evanphx/json-patch.v4 v4.12.0 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
	k8s.io/cli-runtime v0.32.1 // indirect
	k8s.io/cloud-provider v0.0.0 // indirect
	k8s.io/component-helpers v0.32.1 // indirect
	k8s.io/controller-manager v0.32.1 // indirect
	k8s.io/cri-client v0.0.0 // indirect
	k8s.io/csi-translation-lib v0.0.0 // indirect
	k8s.io/dynamic-resource-allocation v0.0.0 // indirect
	k8s.io/gengo/v2 v2.0.0-20250106234829-0359904fc2a6 // indirect
	k8s.io/kms v0.32.1 // indirect
	k8s.io/kube-scheduler v0.0.0 // indirect
	k8s.io/mount-utils v0.0.0 // indirect
	k8s.io/pod-security-admission v0.0.0 // indirect
	sigs.k8s.io/apiserver-network-proxy/konnectivity-client v0.31.0 // indirect
	sigs.k8s.io/kustomize/api v0.18.0 // indirect
	sigs.k8s.io/kustomize/kyaml v0.18.1 // indirect
)

require (
	4d63.com/gochecknoglobals v0.2.1 // indirect
	github.com/Antonboom/errname v1.0.0 // indirect
	github.com/Antonboom/nilnil v1.0.1 // indirect
	github.com/Djarvur/go-err113 v0.0.0-20210108212216-aea10b59be24 // indirect
	github.com/InVisionApp/go-logger v1.0.1 // indirect
	github.com/ajeddeloh/go-json v0.0.0-20200220154158-5ae607161559 // indirect
	github.com/alexkohler/prealloc v1.0.0 // indirect
	github.com/alingse/asasalint v0.0.11 // indirect
	github.com/ashanbrown/forbidigo v1.6.0 // indirect
	github.com/ashanbrown/makezero v1.2.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/bkielbasa/cyclop v1.2.3 // indirect
	github.com/blang/semver/v4 v4.0.0 // indirect
	github.com/blizzy78/varnamelen v0.8.0 // indirect
	github.com/breml/bidichk v0.3.2 // indirect
	github.com/breml/errchkjson v0.4.0 // indirect
	github.com/butuzov/ireturn v0.3.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/charithe/durationcheck v0.0.10 // indirect
	github.com/chavacava/garif v0.1.0 // indirect
	github.com/containers/libtrust v0.0.0-20230121012942-c1716e8a8d01 // indirect
	github.com/containers/ocicrypt v1.2.1 // indirect
	github.com/coreos/go-json v0.0.0-20230131223807-18775e0fb4fb // indirect
	github.com/coreos/go-systemd v0.0.0-20190719114852-fd7a80b32e1f // indirect
	github.com/coreos/go-systemd/v22 v22.5.0 // indirect
	github.com/coreos/vcontext v0.0.0-20230201181013-d72178a18687 // indirect
	github.com/curioswitch/go-reassign v0.3.0 // indirect
	github.com/daixiang0/gci v0.13.5 // indirect
	github.com/denis-tingaikin/go-header v0.5.0 // indirect
	github.com/docker/distribution v2.8.3+incompatible // indirect
	github.com/docker/docker v27.5.1+incompatible // indirect
	github.com/docker/docker-credential-helpers v0.8.2 // indirect
	github.com/docker/go-connections v0.5.0 // indirect
	github.com/docker/go-units v0.5.0 // indirect
	github.com/emicklei/go-restful/v3 v3.12.1 // indirect
	github.com/ettle/strcase v0.2.0 // indirect
	github.com/evanphx/json-patch v5.6.0+incompatible // indirect
	github.com/evanphx/json-patch/v5 v5.9.11 // indirect
	github.com/fatih/color v1.18.0 // indirect
	github.com/fatih/structtag v1.2.0 // indirect
	github.com/firefart/nonamedreturns v1.0.5 // indirect
	github.com/fzipp/gocyclo v0.6.0 // indirect
	github.com/go-critic/go-critic v0.11.5 // indirect
	github.com/go-logr/logr v1.4.2 // indirect
	github.com/go-openapi/jsonpointer v0.21.0 // indirect
	github.com/go-openapi/jsonreference v0.21.0 // indirect
	github.com/go-openapi/swag v0.23.0 // indirect
	github.com/go-toolsmith/astcast v1.1.0 // indirect
	github.com/go-toolsmith/astcopy v1.1.0 // indirect
	github.com/go-toolsmith/astequal v1.2.0 // indirect
	github.com/go-toolsmith/astfmt v1.1.0 // indirect
	github.com/go-toolsmith/astp v1.1.0 // indirect
	github.com/go-toolsmith/strparse v1.1.0 // indirect
	github.com/go-toolsmith/typep v1.1.0 // indirect
	github.com/go-xmlfmt/xmlfmt v1.1.3 // indirect
	github.com/gobwas/glob v0.2.3 // indirect
	github.com/gofrs/flock v0.12.1 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/groupcache v0.0.0-20210331224755-41bb18bfe9da
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/golangci/dupl v0.0.0-20180902072040-3e9179ac440a // indirect
	github.com/golangci/gofmt v0.0.0-20241223200906-057b0627d9b9 // indirect
	github.com/golangci/misspell v0.6.0 // indirect
	github.com/golangci/revgrep v0.5.3 // indirect
	github.com/golangci/unconvert v0.0.0-20240309020433-c5143eacb3ed // indirect
	github.com/google/go-containerregistry v0.20.2 // indirect
	github.com/google/gofuzz v1.2.0 // indirect
	github.com/gordonklaus/ineffassign v0.1.0 // indirect
	github.com/gorilla/mux v1.8.1 // indirect
	github.com/gostaticanalysis/analysisutil v0.7.1 // indirect
	github.com/gostaticanalysis/comment v1.4.2 // indirect
	github.com/gostaticanalysis/forcetypeassert v0.1.0 // indirect
	github.com/gostaticanalysis/nilerr v0.1.1 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/hashicorp/go-version v1.7.0 // indirect
	github.com/hashicorp/hcl v1.0.0 // indirect
	github.com/hexops/gotextdiff v1.0.3 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jgautheron/goconst v1.7.1 // indirect
	github.com/jingyugao/rowserrcheck v1.1.1 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/julz/importas v0.2.0 // indirect
	github.com/kisielk/errcheck v1.8.0 // indirect
	github.com/klauspost/compress v1.17.11 // indirect
	github.com/kulti/thelper v0.6.3 // indirect
	github.com/kunwardeep/paralleltest v1.0.10 // indirect
	github.com/kyoh86/exportloopref v0.1.11 // indirect
	github.com/ldez/gomoddirectives v0.6.0 // indirect
	github.com/ldez/tagliatelle v0.5.0 // indirect
	github.com/leonklingele/grouper v1.1.2 // indirect
	github.com/letsencrypt/boulder v0.0.0-20240620165639-de9c06129bec // indirect
	github.com/magiconair/properties v1.8.7 // indirect
	github.com/mailru/easyjson v0.9.0 // indirect
	github.com/maratori/testpackage v1.1.1 // indirect
	github.com/matoous/godox v0.0.0-20230222163458-006bad1f9d26 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-runewidth v0.0.16 // indirect
	github.com/mgechev/revive v1.5.1 // indirect
	github.com/mitchellh/go-homedir v1.1.0 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/moby/sys/mountinfo v0.7.2 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/moricho/tparallel v0.3.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/nakabonne/nestif v0.3.1 // indirect
	github.com/nishanths/exhaustive v0.12.0 // indirect
	github.com/nishanths/predeclared v0.2.2 // indirect
	github.com/olekukonko/tablewriter v0.0.5 // indirect
	github.com/opencontainers/image-spec v1.1.0
	github.com/opencontainers/runtime-spec v1.2.0 // indirect
	github.com/pelletier/go-toml/v2 v2.2.3 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/polyfloyd/go-errorlint v1.7.0 // indirect
	github.com/proglottis/gpgme v0.1.4 // indirect
	github.com/prometheus/client_model v0.6.1 // indirect
	github.com/prometheus/common v0.62.0 // indirect
	github.com/prometheus/procfs v0.15.1 // indirect
	github.com/quasilyte/go-ruleguard v0.4.3-0.20240823090925-0fe6f58b47b1 // indirect
	github.com/quasilyte/gogrep v0.5.0 // indirect
	github.com/quasilyte/regex/syntax v0.0.0-20210819130434-b3f0c404a727 // indirect
	github.com/quasilyte/stdinfo v0.0.0-20220114132959-f7386bf02567 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/ryancurrah/gomodguard v1.3.5 // indirect
	github.com/ryanrolds/sqlclosecheck v0.5.1 // indirect
	github.com/sanposhiho/wastedassign/v2 v2.1.0 // indirect
	github.com/sashamelentyev/interfacebloat v1.1.0 // indirect
	github.com/sashamelentyev/usestdlibvars v1.28.0 // indirect
	github.com/securego/gosec/v2 v2.21.4 // indirect
	github.com/shazow/go-diff v0.0.0-20160112020656-b6b7b6733b8c // indirect
	github.com/sigstore/sigstore v1.8.12 // indirect
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/sivchari/containedctx v1.0.3 // indirect
	github.com/sivchari/tenv v1.12.1 // indirect
	github.com/sonatard/noctx v0.1.0 // indirect
	github.com/sourcegraph/go-diff v0.7.0 // indirect
	github.com/spf13/afero v1.11.0 // indirect
	github.com/spf13/cast v1.7.0 // indirect
	github.com/spf13/viper v1.19.0 // indirect
	github.com/ssgreg/nlreturn/v2 v2.2.1 // indirect
	github.com/stbenjam/no-sprintf-host-port v0.2.0 // indirect
	github.com/stretchr/objx v0.5.2 // indirect
	github.com/subosito/gotenv v1.6.0 // indirect
	github.com/tdakkota/asciicheck v0.3.0 // indirect
	github.com/tetafro/godot v1.4.20 // indirect
	github.com/timakin/bodyclose v0.0.0-20241017074812-ed6a65f985e3 // indirect
	github.com/titanous/rocacheck v0.0.0-20171023193734-afe73141d399 // indirect
	github.com/tomarrell/wrapcheck/v2 v2.10.0 // indirect
	github.com/tommy-muehle/go-mnd/v2 v2.5.1 // indirect
	github.com/ultraware/funlen v0.1.0 // indirect
	github.com/ultraware/whitespace v0.1.1 // indirect
	github.com/uudashr/gocognit v1.2.0 // indirect
	github.com/yagipy/maintidx v1.0.0 // indirect
	github.com/yeya24/promlinter v0.3.0 // indirect
	gitlab.com/bosi/decorder v0.4.2 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.27.0 // indirect
	go4.org v0.0.0-20200104003542-c7e774b10ea0 // indirect
	golang.org/x/crypto v0.36.0 // indirect
	golang.org/x/exp v0.0.0-20250103183323-7d7fa50e5329
	golang.org/x/exp/typeparams v0.0.0-20241108190413-2d47ceb2692f // indirect
	golang.org/x/mod v0.22.0 // indirect
	golang.org/x/oauth2 v0.25.0 // indirect
	golang.org/x/sync v0.12.0
	golang.org/x/sys v0.31.0 // indirect
	golang.org/x/term v0.30.0 // indirect
	golang.org/x/text v0.23.0 // indirect
	golang.org/x/tools v0.29.0 // indirect
	google.golang.org/protobuf v1.36.4 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/ini.v1 v1.67.0 // indirect
	gopkg.in/yaml.v2 v2.4.0
	gopkg.in/yaml.v3 v3.0.1 // indirect
	honnef.co/go/tools v0.5.1 // indirect
	k8s.io/apiserver v0.32.1
	k8s.io/klog/v2 v2.130.1
	k8s.io/kube-aggregator v0.32.1 // indirect
	k8s.io/kube-openapi v0.0.0-20241212222426-2c72e554b1e7 // indirect
	mvdan.cc/gofumpt v0.7.0 // indirect
	mvdan.cc/unparam v0.0.0-20240528143540-8a5130ca722f // indirect
	sigs.k8s.io/json v0.0.0-20241014173422-cfa47c3a1cc8 // indirect
	sigs.k8s.io/kube-storage-version-migrator v0.0.6-0.20230721195810-5c8923c5ff96 // indirect
	sigs.k8s.io/structured-merge-diff/v4 v4.6.0 // indirect
	sigs.k8s.io/yaml v1.4.0
)

replace (
	github.com/onsi/ginkgo/v2 => github.com/openshift/onsi-ginkgo/v2 v2.6.1-0.20241205171354-8006f302fd12

	k8s.io/api => github.com/openshift/kubernetes/staging/src/k8s.io/api v0.0.0-20250131233843-55625722a4b8
	k8s.io/apiextensions-apiserver => github.com/openshift/kubernetes/staging/src/k8s.io/apiextensions-apiserver v0.0.0-20250131233843-55625722a4b8
	k8s.io/apimachinery => github.com/openshift/kubernetes/staging/src/k8s.io/apimachinery v0.0.0-20250131233843-55625722a4b8
	k8s.io/apiserver => github.com/openshift/kubernetes/staging/src/k8s.io/apiserver v0.0.0-20250131233843-55625722a4b8
	k8s.io/cli-runtime => github.com/openshift/kubernetes/staging/src/k8s.io/cli-runtime v0.0.0-20250131233843-55625722a4b8
	k8s.io/client-go => github.com/openshift/kubernetes/staging/src/k8s.io/client-go v0.0.0-20250131233843-55625722a4b8
	k8s.io/cloud-provider => github.com/openshift/kubernetes/staging/src/k8s.io/cloud-provider v0.0.0-20250131233843-55625722a4b8
	k8s.io/cluster-bootstrap => github.com/openshift/kubernetes/staging/src/k8s.io/cluster-bootstrap v0.0.0-20250131233843-55625722a4b8
	k8s.io/code-generator => github.com/openshift/kubernetes/staging/src/k8s.io/code-generator v0.0.0-20250131233843-55625722a4b8
	k8s.io/component-base => github.com/openshift/kubernetes/staging/src/k8s.io/component-base v0.0.0-20250131233843-55625722a4b8
	k8s.io/component-helpers => github.com/openshift/kubernetes/staging/src/k8s.io/component-helpers v0.0.0-20250131233843-55625722a4b8
	k8s.io/controller-manager => github.com/openshift/kubernetes/staging/src/k8s.io/controller-manager v0.0.0-20250131233843-55625722a4b8
	k8s.io/cri-api => github.com/openshift/kubernetes/staging/src/k8s.io/cri-api v0.0.0-20250131233843-55625722a4b8
	k8s.io/cri-client => github.com/openshift/kubernetes/staging/src/k8s.io/cri-client v0.0.0-20250131233843-55625722a4b8
	k8s.io/csi-translation-lib => github.com/openshift/kubernetes/staging/src/k8s.io/csi-translation-lib v0.0.0-20250131233843-55625722a4b8
	k8s.io/dynamic-resource-allocation => github.com/openshift/kubernetes/staging/src/k8s.io/dynamic-resource-allocation v0.0.0-20250131233843-55625722a4b8
	k8s.io/endpointslice => github.com/openshift/kubernetes/staging/src/k8s.io/endpointslice v0.0.0-20250131233843-55625722a4b8
	k8s.io/externaljwt => github.com/openshift/kubernetes/staging/src/k8s.io/externaljwt v0.0.0-20250131233843-55625722a4b8
	k8s.io/kube-aggregator => github.com/openshift/kubernetes/staging/src/k8s.io/kube-aggregator v0.0.0-20250131233843-55625722a4b8
	k8s.io/kube-controller-manager => github.com/openshift/kubernetes/staging/src/k8s.io/kube-controller-manager v0.0.0-20250131233843-55625722a4b8
	k8s.io/kube-proxy => github.com/openshift/kubernetes/staging/src/k8s.io/kube-proxy v0.0.0-20250131233843-55625722a4b8
	k8s.io/kube-scheduler => github.com/openshift/kubernetes/staging/src/k8s.io/kube-scheduler v0.0.0-20250131233843-55625722a4b8
	k8s.io/kubectl => github.com/openshift/kubernetes/staging/src/k8s.io/kubectl v0.0.0-20250131233843-55625722a4b8
	k8s.io/kubelet => github.com/openshift/kubernetes/staging/src/k8s.io/kubelet v0.0.0-20250131233843-55625722a4b8
	k8s.io/kubernetes => github.com/openshift/kubernetes v1.30.1-0.20250131233843-55625722a4b8
	k8s.io/metrics => github.com/openshift/kubernetes/staging/src/k8s.io/metrics v0.0.0-20250131233843-55625722a4b8
	k8s.io/mount-utils => github.com/openshift/kubernetes/staging/src/k8s.io/mount-utils v0.0.0-20250131233843-55625722a4b8
	k8s.io/pod-security-admission => github.com/openshift/kubernetes/staging/src/k8s.io/pod-security-admission v0.0.0-20250131233843-55625722a4b8
	k8s.io/sample-apiserver => github.com/openshift/kubernetes/staging/src/k8s.io/sample-apiserver v0.0.0-20250131233843-55625722a4b8
	k8s.io/sample-cli-plugin => github.com/openshift/kubernetes/staging/src/k8s.io/sample-cli-plugin v0.0.0-20250131233843-55625722a4b8
	k8s.io/sample-controller => github.com/openshift/kubernetes/staging/src/k8s.io/sample-controller v0.0.0-20250131233843-55625722a4b8
)

// The cadvisor version used in k8s v1.32.1 (v0.51.0) relies on code present on this version
// This can be removed once it's no longer used in o/k
replace github.com/containerd/errdefs => github.com/containerd/errdefs v0.1.0
