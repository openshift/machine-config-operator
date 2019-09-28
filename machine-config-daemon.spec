%define debug_package %{nil}
%global commit          4e75a8f20e5cf44374fd1bf3b3df997b8689d3ff
%global shortcommit     %(c=%{commit}; echo ${c:0:7})

Name:           machine-config-daemon
Version:        4.0.0
Release:        1.rhaos4.2.git%{shortcommit}%{?dist}
Summary:        https://github.com/openshift/machine-config-operator
License:        ASL 2.0
URL:            https://github.com/openshift/machine-config-operator
Source0:        https://github.com/openshift/machine-config-operator/archive/%{commit}/machine-config-operator-%{shortcommit}.tar.gz

BuildRequires:  git
BuildRequires:  %{?go_compiler:compiler(go-compiler)}%{!?go_compiler:golang >= 1.6.2}

%description
%{summary}

%prep
%autosetup -Sgit -n machine-config-operator-%{commit}
mkdir -p src/github.com/openshift/
ln -sr . src/github.com/openshift/machine-config-operator

%build
export GOPATH=`pwd`
cd src/github.com/openshift/machine-config-operator
env VERSION_OVERRIDE=%{version} SOURCE_GIT_COMMIT=%{commit} make daemon

%install
install -D -m 0755 _output/linux/*/%{name} $RPM_BUILD_ROOT/usr/libexec/%{name}
install -D -m 0755 cmd/machine-config-daemon/pivot.sh $RPM_BUILD_ROOT/%{_bindir}/pivot

%files
%license LICENSE
%doc docs/README.md
%{_libexecdir}/%{name}
%{_bindir}/pivot
