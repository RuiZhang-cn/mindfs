import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import ts from "typescript";
import vm from "node:vm";

const sourcePath = path.resolve("src/services/replyMetrics.ts");
const source = fs.readFileSync(sourcePath, "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: {
    module: ts.ModuleKind.CommonJS,
    target: ts.ScriptTarget.ES2020,
  },
}).outputText;

const sandbox = {
  exports: {},
  module: { exports: {} },
};
vm.runInNewContext(compiled, sandbox, { filename: sourcePath });

const { computeTokensPerSecond, formatCacheHitPercent, formatTokensPerSecond, formatCostAmount } =
  sandbox.exports;

assert.equal(computeTokensPerSecond(1_000, 10_000), 100);
assert.equal(computeTokensPerSecond(120, 3_000), 40);
assert.equal(computeTokensPerSecond(500, 999), null);
assert.equal(computeTokensPerSecond(500, 0), null);
assert.equal(computeTokensPerSecond(0, 5_000), null);
assert.equal(computeTokensPerSecond(Number.NaN, 5_000), null);
assert.equal(computeTokensPerSecond(500, Number.NaN), null);
assert.equal(computeTokensPerSecond(500, null), null);

assert.equal(formatTokensPerSecond(100), "100.0 tok/s");
assert.equal(formatTokensPerSecond(7.256), "7.3 tok/s");
assert.equal(formatTokensPerSecond(null), "");
assert.equal(formatTokensPerSecond(0), "");

assert.equal(formatCostAmount(0.42, "USD"), "$0.42");
assert.equal(formatCostAmount(0.0421, "USD"), "$0.0421");
assert.equal(formatCostAmount(0.0425120375, "USD"), "$0.0425");
assert.equal(formatCostAmount(0.0042, "USD"), "$0.0042");
assert.equal(formatCostAmount(0.25, "cny"), "¥0.25");
assert.equal(formatCostAmount(1.5, "eur"), "€1.50");
assert.equal(formatCostAmount(0.5, "XYZ"), "0.50 XYZ");
assert.equal(formatCostAmount(0.5), "$0.50");
assert.equal(formatCostAmount(0, "USD"), "");
assert.equal(formatCostAmount(undefined, "USD"), "");

assert.equal(formatCacheHitPercent(9_000, 10_000), "90.0%");
assert.equal(formatCacheHitPercent(9_995, 10_000), "99.9%");
assert.equal(formatCacheHitPercent(10_000, 10_000), "100%");
assert.equal(formatCacheHitPercent(0, 10_000), "0%");
assert.equal(formatCacheHitPercent(1, 1_000_000), "<0.1%");
assert.equal(formatCacheHitPercent(100, 0), "");
assert.equal(formatCacheHitPercent(Number.NaN, 10), "");
