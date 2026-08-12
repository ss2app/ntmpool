// SPDX-License-Identifier: Apache-2.0
//! noidpow — ParanO(1)d (NOID) Poseidon2b PoW 的 C-ABI 封装（NTMPool 池端 share 验证用）。
//!
//! 同源铁律：本 crate **不实现任何算法**，digest 只经官方
//! `noid_chain::consensus::pow::poseidon_pow_digest_from_fields`，target 判定只经官方
//! `noid_chain::consensus::difficulty::le256_lt`（256-bit 小端严格 `<`，等于拒绝）。
//!
//! wire 口径（与官方 noid_extminer 逐行同款，main.rs:274-275 / :189）：
//! - `fields_le`：256 字节 = 16 个字段 × 16 字节，每字段 `u128::from_le_bytes`；
//! - `nonce_le`：16 字节 LE u128，验证时覆写 `fields[POW_NONCE_FIELD_INDEX=10]`；
//! - `out` digest：官方 `BlockHash = [u8; 32]` 原样字节。
//!
//! 线程安全：Poseidon2b 纯函数；上游全局状态仅为 `OnceLock` 只写一次的懒表
//! （轮常数展开表、CPU 特性探测缓存），可安全并发调用，无需加锁。
//!
//! panic 边界：算法路径对任意 256B/16B 输入均无 panic 分支；Rust ≥1.81 下 panic
//! 穿越 `extern "C"` 边界会直接 abort（定义行为），不会把 UB 泄给 Go 侧。

use noid_chain::consensus::difficulty::le256_lt;
use noid_chain::consensus::pow::{
    poseidon_pow_digest_from_fields, PowHeaderFields, POW_HEADER_FIELD_COUNT,
    POW_NONCE_FIELD_INDEX,
};
use noid_core::Block128;

/// pow_fields_hex 解码后的 wire 字节数：16 字段 × 16B LE = 256。
pub const POW_FIELDS_WIRE_BYTES: usize = POW_HEADER_FIELD_COUNT * 16;
/// 矿工提交的 nonce wire 字节数（LE u128）。
pub const NONCE_WIRE_BYTES: usize = 16;

// 共识常量哨兵：上游若改字段表布局，这里编译期就炸，不给"静默挖废块"留机会。
const _: () = assert!(POW_HEADER_FIELD_COUNT == 16);
const _: () = assert!(POW_NONCE_FIELD_INDEX == 10);

/// wire 字节 → 官方字段表，并把 fields[10] 覆写为提交的 nonce。
/// 解码逐行对应官方 noid_extminer `decode_pow_fields_hex`（main.rs:274-275）。
fn fields_from_wire(fields_le: &[u8], nonce_le: &[u8]) -> PowHeaderFields {
    assert_eq!(fields_le.len(), POW_FIELDS_WIRE_BYTES);
    assert_eq!(nonce_le.len(), NONCE_WIRE_BYTES);
    let mut fields = [Block128::from(0u128); POW_HEADER_FIELD_COUNT];
    for (i, chunk) in fields_le.chunks_exact(16).enumerate() {
        fields[i] = Block128::from(u128::from_le_bytes(chunk.try_into().unwrap()));
    }
    fields[POW_NONCE_FIELD_INDEX] =
        Block128::from(u128::from_le_bytes(nonce_le.try_into().unwrap()));
    fields
}

/// 安全 Rust 入口（单元测试 / gen_kat 交叉验证用），与 FFI 导出走同一条路。
pub fn digest_wire(fields_le: &[u8; POW_FIELDS_WIRE_BYTES], nonce_le: &[u8; NONCE_WIRE_BYTES]) -> [u8; 32] {
    poseidon_pow_digest_from_fields(&fields_from_wire(fields_le, nonce_le))
}

/// C ABI：digest = PoseidonPoW(fields[10]:=nonce)。
///
/// # Safety
/// `fields_le` 必须指向 256 字节、`nonce_le` 16 字节、`out` 32 字节可写内存。
/// 调用方（Go cgo）传固定长度栈数组，长度/对齐由调用方保证（u8 无对齐要求）。
#[no_mangle]
pub unsafe extern "C" fn noid_pow_digest(fields_le: *const u8, nonce_le: *const u8, out: *mut u8) {
    let fields = core::slice::from_raw_parts(fields_le, POW_FIELDS_WIRE_BYTES);
    let nonce = core::slice::from_raw_parts(nonce_le, NONCE_WIRE_BYTES);
    let digest = poseidon_pow_digest_from_fields(&fields_from_wire(fields, nonce));
    core::ptr::copy_nonoverlapping(digest.as_ptr(), out, 32);
}

/// C ABI：返回 1 = digest < target（官方 le256_lt，256-bit LE 严格 `<`，等于拒绝）；0 = 否。
///
/// # Safety
/// `fields_le` 256B / `nonce_le` 16B / `target_le` 32B，同上。
#[no_mangle]
pub unsafe extern "C" fn noid_pow_check(
    fields_le: *const u8,
    nonce_le: *const u8,
    target_le: *const u8,
) -> i32 {
    let fields = core::slice::from_raw_parts(fields_le, POW_FIELDS_WIRE_BYTES);
    let nonce = core::slice::from_raw_parts(nonce_le, NONCE_WIRE_BYTES);
    let target: &[u8; 32] = core::slice::from_raw_parts(target_le, 32)
        .try_into()
        .unwrap();
    let digest = poseidon_pow_digest_from_fields(&fields_from_wire(fields, nonce));
    if le256_lt(&digest, target) {
        1
    } else {
        0
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// 确定性伪随机（splitmix64）——测试要可复现。
    fn splitmix64(state: &mut u64) -> u64 {
        *state = state.wrapping_add(0x9E37_79B9_7F4A_7C15);
        let mut z = *state;
        z = (z ^ (z >> 30)).wrapping_mul(0xBF58_476D_1CE4_E5B9);
        z = (z ^ (z >> 27)).wrapping_mul(0x94D0_49BB_1331_11EB);
        z ^ (z >> 31)
    }

    fn rand_bytes<const N: usize>(state: &mut u64) -> [u8; N] {
        let mut out = [0u8; N];
        for chunk in out.chunks_mut(8) {
            let v = splitmix64(state).to_le_bytes();
            chunk.copy_from_slice(&v[..chunk.len()]);
        }
        out
    }

    /// FFI 导出与官方函数直调必须逐字节一致（含 nonce 覆写语义）。
    #[test]
    fn ffi_digest_matches_official_direct_call() {
        let mut st = 0x4E4F_4944_5F4C_4942u64; // "NOID_LIB"
        for _ in 0..32 {
            let fields_bytes: [u8; POW_FIELDS_WIRE_BYTES] = rand_bytes(&mut st);
            let nonce_bytes: [u8; NONCE_WIRE_BYTES] = rand_bytes(&mut st);

            // 官方口径：decode → 覆写 fields[10] → poseidon_pow_digest_from_fields。
            let mut fields = [Block128::from(0u128); POW_HEADER_FIELD_COUNT];
            for (i, chunk) in fields_bytes.chunks_exact(16).enumerate() {
                fields[i] = Block128::from(u128::from_le_bytes(chunk.try_into().unwrap()));
            }
            fields[POW_NONCE_FIELD_INDEX] =
                Block128::from(u128::from_le_bytes(nonce_bytes));
            let want = poseidon_pow_digest_from_fields(&fields);

            let mut got = [0u8; 32];
            unsafe {
                noid_pow_digest(fields_bytes.as_ptr(), nonce_bytes.as_ptr(), got.as_mut_ptr())
            };
            assert_eq!(got, want);
            assert_eq!(digest_wire(&fields_bytes, &nonce_bytes), want);
        }
    }

    /// check 的边界语义：target=digest+1 → 1；=digest → 0（严格 <）；=digest-1 → 0。
    #[test]
    fn check_boundary_semantics() {
        let mut st = 0x4E4F_4944_5F43_484Bu64; // "NOID_CHK"
        let fields_bytes: [u8; POW_FIELDS_WIRE_BYTES] = rand_bytes(&mut st);
        let nonce_bytes: [u8; NONCE_WIRE_BYTES] = rand_bytes(&mut st);
        let digest = digest_wire(&fields_bytes, &nonce_bytes);

        let le_add1 = |mut v: [u8; 32]| {
            for b in v.iter_mut() {
                let (nv, carry) = b.overflowing_add(1);
                *b = nv;
                if !carry {
                    break;
                }
            }
            v
        };
        let le_sub1 = |mut v: [u8; 32]| {
            for b in v.iter_mut() {
                let (nv, borrow) = b.overflowing_sub(1);
                *b = nv;
                if !borrow {
                    break;
                }
            }
            v
        };

        let check = |target: &[u8; 32]| unsafe {
            noid_pow_check(fields_bytes.as_ptr(), nonce_bytes.as_ptr(), target.as_ptr())
        };
        assert_eq!(check(&le_add1(digest)), 1, "digest < digest+1 必须为真");
        assert_eq!(check(&digest), 0, "等于必须拒绝（严格 <）");
        assert_eq!(check(&le_sub1(digest)), 0, "digest < digest-1 必须为假");
        assert_eq!(check(&[0xFF; 32]), 1, "max target 必须接受");
        assert_eq!(check(&[0x00; 32]), 0, "zero target 必须拒绝");
    }
}
