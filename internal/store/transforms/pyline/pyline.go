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
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

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

// dictLookup maps a stripped line to its encoded byte value.
// The encoded byte skips 0x00 (marker) and 0x0a (newline) to avoid
// conflicting with the line delimiter used by bufio.Reader.ReadString.
var dictLookup map[string]byte

// encodeIndex maps a dictionary index to an encoded byte, skipping 0x00
// and 0x0a.
func encodeIndex(i int) byte {
	b := i + 1 // skip 0x00
	if b >= 0x0a {
		b++ // skip 0x0a
	}
	return byte(b)
}

// decodeIndex maps an encoded byte back to a dictionary index.
func decodeIndex(b byte) int {
	i := int(b)
	if i > 0x0a {
		i-- // undo 0x0a skip
	}
	i-- // undo 0x00 skip
	return i
}

func init() {
	dictLookup = make(map[string]byte, len(dict))
	for i, s := range dict {
		dictLookup[s] = encodeIndex(i)
	}
}

// PyLineSubst implements the py-line-subst/v1 transform.
type PyLineSubst struct{}

// NewPyLineSubst returns a new PyLineSubst transform.
func NewPyLineSubst() *PyLineSubst { return &PyLineSubst{} }

// ID returns the stable transform identifier.
func (p *PyLineSubst) ID() marc.TransformID { return pyLineSubstID }

// Applicable returns true for .py files with size > 0.
func (p *PyLineSubst) Applicable(_ context.Context, e marc.Entry, facts marc.Facts) bool {
	return filepath.Ext(e.RelPath) == ".py" && facts.Size > 0
}

// CostEstimate returns estimated gain and CPU cost. Mirrors goline's
// 10% gain heuristic; pure-CPU work scales linearly with file size.
func (p *PyLineSubst) CostEstimate(_ marc.Entry, facts marc.Facts) (gainBytes, cpuUnits int64) {
	return facts.Size / 10, facts.Size / 1024
}

// Apply reads src line-by-line, replacing dictionary-matched lines with
// 2-byte tokens (\x00 + index). The result is written as a single blob.
// Returns handled=false if the content contains NUL bytes.
func (p *PyLineSubst) Apply(ctx context.Context, _ marc.Entry, _ marc.Facts, src io.Reader, sink marc.BlobSink) (marc.Result, bool, error) {
	reader := bufio.NewReaderSize(src, 64*1024)
	var buf bytes.Buffer

	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			if strings.ContainsRune(line, 0x00) {
				return marc.Result{}, false, nil
			}

			hasNewline := strings.HasSuffix(line, "\n")
			content := line
			if hasNewline {
				content = line[:len(line)-1]
			}

			stripped := strings.TrimLeft(content, "\t ")
			prefix := content[:len(content)-len(stripped)]

			if idx, ok := dictLookup[stripped]; ok {
				buf.WriteString(prefix)
				buf.WriteByte(0x00)
				buf.WriteByte(idx)
			} else {
				buf.WriteString(content)
			}
			if hasNewline {
				buf.WriteByte('\n')
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return marc.Result{}, false, fmt.Errorf("py-line-subst: read: %w", err)
		}
	}

	id, err := sink.Write(ctx, bytes.NewReader(buf.Bytes()))
	if err != nil {
		return marc.Result{}, false, fmt.Errorf("py-line-subst: write blob: %w", err)
	}

	return marc.Result{BlobIDs: []marc.BlobID{id}}, true, nil
}

// Reverse reconstructs the original .py file from the substituted blob.
func (p *PyLineSubst) Reverse(_ context.Context, r marc.Result, blobs marc.BlobReader, dst io.Writer) error {
	if len(r.BlobIDs) == 0 {
		return nil
	}

	rc, err := blobs.Open(r.BlobIDs[0])
	if err != nil {
		return fmt.Errorf("py-line-subst: open blob: %w", err)
	}
	defer func() { _ = rc.Close() }()

	reader := bufio.NewReaderSize(rc, 64*1024)
	w := bufio.NewWriter(dst)

	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			hasNewline := strings.HasSuffix(line, "\n")
			content := line
			if hasNewline {
				content = line[:len(line)-1]
			}

			if idx := strings.IndexByte(content, 0x00); idx >= 0 {
				prefix := content[:idx]
				if idx+1 < len(content) {
					dictIdx := decodeIndex(content[idx+1])
					if dictIdx >= 0 && dictIdx < len(dict) {
						if _, err := w.WriteString(prefix); err != nil {
							return err
						}
						if _, err := w.WriteString(dict[dictIdx]); err != nil {
							return err
						}
					} else {
						if _, err := w.WriteString(content); err != nil {
							return err
						}
					}
				} else {
					if _, err := w.WriteString(content); err != nil {
						return err
					}
				}
			} else {
				if _, err := w.WriteString(content); err != nil {
					return err
				}
			}
			if hasNewline {
				if err := w.WriteByte('\n'); err != nil {
					return err
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("py-line-subst: read blob: %w", err)
		}
	}

	return w.Flush()
}
