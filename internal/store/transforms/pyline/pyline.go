// Package pyline implements the py-line-subst/v1 transform, a lossless
// line-level substitution for .py files. Frequently-repeated lines are
// replaced with 2-byte tokens (\x00 + 1-byte index) before zstd compression,
// exploiting the high line-level redundancy found in Python source corpora.
//
// The substitution is done after stripping leading whitespace (tabs and
// spaces): Python's significant indentation is preserved verbatim, and the
// tokenized line is recognised regardless of its indentation level.
package pyline

import (
	"path/filepath"

	"github.com/arhuman/metarc-go/internal/store/transforms/linesubst"
	"github.com/arhuman/metarc-go/pkg/marc"
)

const pyLineSubstID marc.TransformID = "py-line-subst/v1"

// dict contains frequent Python source lines (whitespace-stripped),
// indexed by token byte value. Immutable for py-line-subst/v1.
//
// The list was assembled by sampling high-frequency lines from a curated
// corpus (CPython stdlib, numpy, pytest, requests). Tokens cover three
// classes: stdlib imports, common idioms (constructors, special-method
// stubs, return patterns), and license-header lines that recur across
// most projects.
var dict = [...]string{
	`# -*- coding: utf-8 -*-`,
	`# Copyright 201`,
	`# Copyright 202`,
	`# Licensed under the Apache License, Version 2.0 (the "License");`,
	`# Licensed to the Apache Software Foundation (ASF) under one`,
	`# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.`,
	`# distributed under the License is distributed on an "AS IS" BASIS,`,
	`# limitations under the License.`,
	`# noqa`,
	`# noqa: E501`,
	`# pragma: no cover`,
	`# pylint: disable=invalid-name`,
	`# type: ignore`,
	`# type: ignore[attr-defined]`,
	`# type: ignore[import]`,
	`#!/usr/bin/env python`,
	`#!/usr/bin/env python3`,
	`@abstractmethod`,
	`@classmethod`,
	`@dataclass`,
	`@dataclass(frozen=True)`,
	`@deprecated`,
	`@override`,
	`@pytest.fixture`,
	`@pytest.fixture(scope="module")`,
	`@pytest.fixture(scope="session")`,
	`@pytest.mark.asyncio`,
	`@pytest.mark.parametrize`,
	`@pytest.mark.skip`,
	`@pytest.mark.skipif`,
	`@property`,
	`@staticmethod`,
	`assert False`,
	`assert True`,
	`break`,
	`continue`,
	`def __call__(self, *args, **kwargs):`,
	`def __enter__(self):`,
	`def __eq__(self, other):`,
	`def __exit__(self, exc_type, exc_val, exc_tb):`,
	`def __getitem__(self, key):`,
	`def __hash__(self):`,
	`def __init__(self):`,
	`def __init__(self, *args, **kwargs):`,
	`def __iter__(self):`,
	`def __len__(self):`,
	`def __next__(self):`,
	`def __repr__(self):`,
	`def __str__(self):`,
	`def setUp(self):`,
	`def tearDown(self):`,
	`else:`,
	`finally:`,
	`from __future__ import annotations`,
	`from abc import ABC, abstractmethod`,
	`from collections import OrderedDict`,
	`from collections import defaultdict`,
	`from collections import namedtuple`,
	`from contextlib import contextmanager`,
	`from copy import deepcopy`,
	`from dataclasses import dataclass`,
	`from dataclasses import dataclass, field`,
	`from datetime import datetime`,
	`from datetime import datetime, timedelta`,
	`from functools import lru_cache`,
	`from functools import wraps`,
	`from pathlib import Path`,
	`from typing import Any`,
	`from typing import Any, Dict, List, Optional`,
	`from typing import Callable`,
	`from typing import Dict, List, Optional`,
	`from typing import List`,
	`from typing import List, Optional`,
	`from typing import Optional`,
	`from typing import TYPE_CHECKING`,
	`from typing import Tuple`,
	`from typing import Union`,
	`from unittest import TestCase`,
	`from unittest.mock import MagicMock`,
	`from unittest.mock import Mock`,
	`from unittest.mock import patch`,
	`http://www.apache.org/licenses/LICENSE-2.0`,
	`if __name__ == "__main__":`,
	`if not isinstance(other, type(self)):`,
	`if not self:`,
	`if self is None:`,
	`import abc`,
	`import argparse`,
	`import asyncio`,
	`import collections`,
	`import contextlib`,
	`import copy`,
	`import dataclasses`,
	`import datetime`,
	`import functools`,
	`import inspect`,
	`import io`,
	`import itertools`,
	`import json`,
	`import logging`,
	`import math`,
	`import numpy as np`,
	`import operator`,
	`import os`,
	`import pathlib`,
	`import pickle`,
	`import pytest`,
	`import random`,
	`import re`,
	`import shutil`,
	`import struct`,
	`import subprocess`,
	`import sys`,
	`import tempfile`,
	`import time`,
	`import traceback`,
	`import typing`,
	`import unittest`,
	`import warnings`,
	`logger = logging.getLogger(__name__)`,
	`pass`,
	`raise NotImplementedError`,
	`raise NotImplementedError()`,
	`raise TypeError`,
	`raise ValueError`,
	`return False`,
	`return None`,
	`return NotImplemented`,
	`return True`,
	`return cls(`,
	`return result`,
	`return self`,
	`return self._value`,
	`return value`,
	`self._lock = threading.Lock()`,
	`super().__init__()`,
	`super().__init__(*args, **kwargs)`,
	`try:`,
	`unittest.main()`,
}

// dictLookup mirrors the lookup map built by linesubst.New, exposed at
// package level so the package's own tests can verify dictionary integrity.
var dictLookup = buildDictLookup()

func buildDictLookup() map[string]byte {
	m := make(map[string]byte, len(dict))
	for i, s := range dict {
		m[s] = linesubst.EncodeIndex(i)
	}
	return m
}

func encodeIndex(i int) byte { return linesubst.EncodeIndex(i) }
func decodeIndex(b byte) int { return linesubst.DecodeIndex(b) }

// PyLineSubst implements the py-line-subst/v1 transform.
type PyLineSubst struct {
	*linesubst.LineSubst
}

// NewPyLineSubst returns a new PyLineSubst transform.
func NewPyLineSubst() *PyLineSubst {
	return &PyLineSubst{
		LineSubst: linesubst.New(pyLineSubstID, dict[:], applicable),
	}
}

func applicable(e marc.Entry, facts marc.Facts) bool {
	return filepath.Ext(e.RelPath) == ".py" && facts.Size > 0
}
