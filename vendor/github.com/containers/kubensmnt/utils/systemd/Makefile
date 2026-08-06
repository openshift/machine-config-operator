TEST_TARGETS := test-all test-integration shellcheck

.PHONY: help install wrappers envdropins $(TEST_TARGETS)

help:
	@echo "Run 'sudo make install' to install kubens.service"
	@echo
	@echo "Adding WRAP_SERVICES=... will also create drop-in wrappers"
	@echo "which wraps each service's ExecStart line with kubensenter:"
	@echo "  make install WRAP_SERVICES=\"k3s.service other.service\""
	@echo
	@echo "Adding ENV_SERVICES=... will also create drop-ins which set"
	@echo "$$KUBENSMNT for use with services that can enter the mount"
	@echo "namespace on their own:"
	@echo "  make install ENV_SERVICES=\"crio.service\""

$(TEST_TARGETS):
	$(MAKE) -C test $@

install: /etc/systemd/system/kubens.service wrappers envdropins
	
/etc/systemd/system/kubens.service: kubens.service
	install "$^" "$@"
	systemctl daemon-reload
	systemctl enable kubens.service

wrappers:
	@if [ -n "$(WRAP_SERVICES)" ]; then \
		./mkWrapperDropin $(WRAP_SERVICES); \
		systemctl daemon-reload; \
	fi

ENV_DROPINS=$(foreach svc,$(ENV_SERVICES),/etc/systemd/system/$(svc).d/00-kubensmnt-env.conf)

envdropins: $(ENV_DROPINS)

/etc/systemd/system/%.d/00-kubensmnt-env.conf: ./kubens-dropin.conf
	install -D "$^" "$@"
	systemctl daemon-reload
