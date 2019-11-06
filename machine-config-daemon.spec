%define debug_package %{nil}
%{!?commit:
%global commit          1bb46853cd115d5545aa6fd9f03fde92acce16f6
}
%global shortcommit     %(c=%{commit}; echo ${c:0:7})

Name:           machine-config-daemon
Version:        4.0.0
Release:        1.rhaos4.3.git%{shortcommit}%{?dist}
Summary:        https://github.com/openshift/machine-config-operator
License:        ASL 2.0
URL:            https://github.com/openshift/machine-config-operator
Source0:        https://github.com/openshift/machine-config-operator/archive/%{commit}/machine-config-operator-%{shortcommit}.tar.gz

BuildRequires:  git
BuildRequires:  %{?go_compiler:compiler(go-compiler)}%{!?go_compiler:golang >= 1.12}

%description
%{summary}

%prep
%autosetup -Sgit -n machine-config-operator-%{commit}

%build
# By default go build doesn't uses vendored packags with Go modules
# Should be fixed with Go 1.14 - https://github.com/golang/go/issues/33848
env VERSION_OVERRIDE=%{version} SOURCE_GIT_COMMIT=%{commit} GOFLAGS='-mod=vendor' WHAT='machine-config-daemon' ./hack/build-go.sh

%install
install -D -m 0755 _output/linux/*/%{name} $RPM_BUILD_ROOT/usr/libexec/%{name}
install -D -m 0755 cmd/machine-config-daemon/pivot.sh $RPM_BUILD_ROOT/%{_bindir}/pivot

%files
%license LICENSE
%doc docs/README.md
%{_libexecdir}/%{name}
%{_bindir}/pivot
