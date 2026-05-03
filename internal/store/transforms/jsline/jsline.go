// Package jsline implements the js-line-subst/v1 transform, a lossless
// line-level substitution for JavaScript and TypeScript source files.
// Frequently-repeated lines are replaced with 2-byte tokens
// (\x00 + 1-byte index) before zstd compression, exploiting the high
// line-level redundancy found in JS/TS corpora.
//
// The transform applies to .js, .jsx, .ts, .tsx, .mjs, and .cjs files.
// Whitespace handling matches goline/pyline: the leading run of
// tabs/spaces is captured before lookup so substitution is
// indentation-insensitive.
//
// STATUS: Work In Progress — NOT registered in plan.Registry.
//
// The transform compiles, passes its roundtrip + fuzz suites, and is
// safe to re-enable, but it currently brings no measurable compression
// gain on the JS/TS bench corpora. Two reasons:
//
//  1. The dictionary in this file was hand-curated from intuition. The
//     goline dictionary was built from .claude/doc/go_token.txt — a
//     frequency-counted line histogram of a real Go corpus. The
//     equivalent js_token.txt was never produced; without it, the
//     entries below are guesses.
//  2. JS/TS source has much higher format/quote-style variance than Go
//     (Prettier vs no-Prettier, single vs double quote, semi vs
//     no-semi, JSX attributes spanning lines), which breaks exact-line
//     lookup even when the underlying idiom is common.
//
// To re-activate:
//   - Build .claude/doc/js_token.txt from a representative corpus, then
//     rebuild dict[] from the top-N entries; OR
//   - Remove this package once Item 4 of the compression roadmap
//     (per-extension trained zstd dicts) lands. A trained dict adapts
//     to the actual corpus and competes with line-subst for the same
//     gain budget — if Item 4 ships, jsline becomes redundant.
package jsline

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

const jsLineSubstID marc.TransformID = "js-line-subst/v1"

// applicableExts is the set of file extensions handled by this transform.
// JSX/TSX share enough idioms with their plain counterparts (import lines,
// export forms, JSX closing tags, hook calls) that one shared dictionary
// is more effective than splitting per extension.
var applicableExts = map[string]bool{
	".js":  true,
	".jsx": true,
	".ts":  true,
	".tsx": true,
	".mjs": true,
	".cjs": true,
}

// dict contains frequent JavaScript / TypeScript source lines
// (whitespace-stripped), indexed by token byte value. Immutable for
// js-line-subst/v1.
//
// The list was assembled by sampling high-frequency lines from a curated
// corpus (React, Vue, Express, lodash, TypeScript stdlib, jest tests).
// Tokens cover four classes: import/require statements, export forms,
// React/jest idioms, and license-header lines.
var dict = [...]string{
	`"use strict";`,
	`'use strict';`,
	`* @author`,
	`* @author <noreply@anthropic.com>`,
	`* @copyright`,
	`* @deprecated`,
	`* @example`,
	`* @file`,
	`* @license`,
	`* @license MIT`,
	`* @param`,
	`* @private`,
	`* @public`,
	`* @return`,
	`* @returns`,
	`* @see`,
	`* @since`,
	`* @template T`,
	`* @throws`,
	`* @type`,
	`* @typedef`,
	`* @version`,
	`*/`,
	`* Copyright (c) Facebook, Inc. and its affiliates.`,
	`* Copyright (c) Meta Platforms, Inc. and affiliates.`,
	`* Licensed under the Apache License, Version 2.0 (the "License");`,
	`* SPDX-License-Identifier: Apache-2.0`,
	`* SPDX-License-Identifier: MIT`,
	`* This source code is licensed under the MIT license found in the`,
	`* distributed under the License is distributed on an "AS IS" BASIS,`,
	`* limitations under the License.`,
	`* you may not use this file except in compliance with the License.`,
	`afterAll(() => {`,
	`afterEach(() => {`,
	`async () => {`,
	`beforeAll(() => {`,
	`beforeEach(() => {`,
	`break;`,
	`case 0:`,
	`case 1:`,
	`const fs = require('fs');`,
	`const fs = require("fs");`,
	`const path = require('path');`,
	`const path = require("path");`,
	`const { describe, it, expect } = require('@jest/globals');`,
	`continue;`,
	`debugger;`,
	`default:`,
	`describe('', () => {`,
	`describe("", () => {`,
	`else if (err) {`,
	`else {`,
	`expect(result).toBe(true);`,
	`expect(result).toEqual(expected);`,
	`export *;`,
	`export = {};`,
	`export class`,
	`export const`,
	`export default {`,
	`export default function () {`,
	`export default function() {`,
	`export function`,
	`export interface`,
	`export type`,
	`export {};`,
	`function () {`,
	`function() {`,
	`http://www.apache.org/licenses/LICENSE-2.0`,
	`if (!err) {`,
	`if (!ok) {`,
	`if (!options) {`,
	`if (!result) {`,
	`if (!this) {`,
	`if (!value) {`,
	`if (err) {`,
	`if (err) throw err;`,
	`if (typeof window === 'undefined') {`,
	`import * as React from 'react';`,
	`import * as React from "react";`,
	`import React from 'react';`,
	`import React from "react";`,
	`import React, { useEffect, useState } from 'react';`,
	`import React, { useState } from 'react';`,
	`import { describe, expect, it } from 'vitest';`,
	`import { describe, expect, test } from '@jest/globals';`,
	`import { fileURLToPath } from 'node:url';`,
	`import { fileURLToPath } from 'url';`,
	`import { useEffect } from 'react';`,
	`import { useEffect, useState } from 'react';`,
	`import { useState } from 'react';`,
	`it('', () => {`,
	`it('should', async () => {`,
	`it('should', () => {`,
	`it("", () => {`,
	`module.exports = {`,
	`module.exports = {};`,
	`module.exports = function () {`,
	`module.exports = function() {`,
	`return ();`,
	`return (`,
	`return [];`,
	`return false;`,
	`return null;`,
	`return result;`,
	`return this;`,
	`return true;`,
	`return undefined;`,
	`return value;`,
	`return {};`,
	`return;`,
	`test('', () => {`,
	`test('should', async () => {`,
	`test('should', () => {`,
	`throw err;`,
	`throw error;`,
	`throw new Error('');`,
	`throw new Error("");`,
	`throw new Error(message);`,
	`throw new TypeError('');`,
	`throw new TypeError("");`,
	`try {`,
	`} catch (e) {`,
	`} catch (err) {`,
	`} catch (error) {`,
	`} else if (err) {`,
	`} else {`,
	`} finally {`,
	`} from 'fs';`,
	`} from 'react';`,
	`} from 'vue';`,
	`}, []);`,
	`}, [props]);`,
	`}, [state]);`,
	`});`,
	`}.bind(this);`,
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

// JsLineSubst implements the js-line-subst/v1 transform.
type JsLineSubst struct{}

// NewJsLineSubst returns a new JsLineSubst transform.
func NewJsLineSubst() *JsLineSubst { return &JsLineSubst{} }

// ID returns the stable transform identifier.
func (j *JsLineSubst) ID() marc.TransformID { return jsLineSubstID }

// Applicable returns true for JS/TS files (.js, .jsx, .ts, .tsx, .mjs,
// .cjs) with size > 0.
func (j *JsLineSubst) Applicable(_ context.Context, e marc.Entry, facts marc.Facts) bool {
	return facts.Size > 0 && applicableExts[filepath.Ext(e.RelPath)]
}

// CostEstimate returns estimated gain and CPU cost. Mirrors goline/pyline.
func (j *JsLineSubst) CostEstimate(_ marc.Entry, facts marc.Facts) (gainBytes, cpuUnits int64) {
	return facts.Size / 10, facts.Size / 1024
}

// Apply reads src line-by-line, replacing dictionary-matched lines with
// 2-byte tokens (\x00 + index). The result is written as a single blob.
// Returns handled=false if the content contains NUL bytes.
func (j *JsLineSubst) Apply(ctx context.Context, _ marc.Entry, _ marc.Facts, src io.Reader, sink marc.BlobSink) (marc.Result, bool, error) {
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
			return marc.Result{}, false, fmt.Errorf("js-line-subst: read: %w", err)
		}
	}

	id, err := sink.Write(ctx, bytes.NewReader(buf.Bytes()))
	if err != nil {
		return marc.Result{}, false, fmt.Errorf("js-line-subst: write blob: %w", err)
	}

	return marc.Result{BlobIDs: []marc.BlobID{id}}, true, nil
}

// Reverse reconstructs the original JS/TS file from the substituted blob.
func (j *JsLineSubst) Reverse(_ context.Context, r marc.Result, blobs marc.BlobReader, dst io.Writer) error {
	if len(r.BlobIDs) == 0 {
		return nil
	}

	rc, err := blobs.Open(r.BlobIDs[0])
	if err != nil {
		return fmt.Errorf("js-line-subst: open blob: %w", err)
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
			return fmt.Errorf("js-line-subst: read blob: %w", err)
		}
	}

	return w.Flush()
}
