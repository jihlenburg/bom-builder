PYTHON ?= python
SPHINXBUILD ?= $(PYTHON) -m sphinx
SOURCEDIR = .
BUILDDIR = _build

.PHONY: api html clean

api:
	rm -rf api/generated
	$(PYTHON) generate_api_index.py

html: api
	$(SPHINXBUILD) -b html $(SOURCEDIR) $(BUILDDIR)/html

clean:
	rm -rf $(BUILDDIR) api/generated
