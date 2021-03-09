ANSIBLE_PLAYBOOK_CMD   = ansible-playbook
ANSIBLE_INVENTORY_FILE = inventories/hosts
ANSIBLE_PLAYBOOK_FILE  = hosts.yml

.PHONY: apply
apply: run-playbook

.PHONY: run-playbook
run-playbook:
	$(ANSIBLE_PLAYBOOK_CMD) --ask-become-pass --inventory-file $(ANSIBLE_INVENTORY_FILE) $(ANSIBLE_PLAYBOOK_FILE)

.PHONY: test-syntax
test-syntax:
	$(ANSIBLE_PLAYBOOK_CMD) --syntax-check --inventory-file $(ANSIBLE_INVENTORY_FILE) $(ANSIBLE_PLAYBOOK_FILE)

.PHONY: test-check
test-check:
	$(ANSIBLE_PLAYBOOK_CMD) --check --inventory-file $(ANSIBLE_INVENTORY_FILE) $(ANSIBLE_PLAYBOOK_FILE)

.PHONY: test
test: test-syntax test-check
