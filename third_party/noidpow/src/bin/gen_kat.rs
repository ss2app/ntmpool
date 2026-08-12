// SPDX-License-Identifier: Apache-2.0
//! gen_kat — NOID Poseidon2b PoW 的 KAT（Known Answer Test）金值生成器。
//!
//! 金值来源 = 官方 `noid_chain::consensus::pow::poseidon_pow_digest_from_fields`。
//! 本 bin **故意不经过** noidpow lib 的 FFI/helper 路径：解码逻辑独立照抄官方
//! noid_extminer（main.rs:274-275），使 KAT 成为对 wrapper 的独立 oracle，
//! 而不是"自己测自己"。
//!
//! 输出：stdout JSON 数组，每条
//!   {"fields_hex": 512 hex（256B，含 index10 处的垃圾字节，被 nonce 覆写——
//!     故意放垃圾，逼测试证明覆写真的生效）,
//!    "nonce_hex": 32 hex（16B LE）,
//!    "digest_hex": 64 hex（32B）}
//!
//! 确定性：固定种子 splitmix64，任何机器重跑输出逐字节相同。

use noid_chain::consensus::pow::{
    poseidon_pow_digest_from_fields, PowHeaderFields, POW_HEADER_FIELD_COUNT,
    POW_NONCE_FIELD_INDEX,
};
use noid_core::Block128;

const FIELDS_BYTES: usize = POW_HEADER_FIELD_COUNT * 16; // 256
const RANDOM_ENTRIES: usize = 64;

fn splitmix64(state: &mut u64) -> u64 {
    *state = state.wrapping_add(0x9E37_79B9_7F4A_7C15);
    let mut z = *state;
    z = (z ^ (z >> 30)).wrapping_mul(0xBF58_476D_1CE4_E5B9);
    z = (z ^ (z >> 27)).wrapping_mul(0x94D0_49BB_1331_11EB);
    z ^ (z >> 31)
}

fn rand_vec(state: &mut u64, n: usize) -> Vec<u8> {
    let mut out = vec![0u8; n];
    for chunk in out.chunks_mut(8) {
        let v = splitmix64(state).to_le_bytes();
        chunk.copy_from_slice(&v[..chunk.len()]);
    }
    out
}

/// 官方解码（逐行对应 noid_extminer main.rs:274-275）。
fn decode_pow_fields(bytes: &[u8]) -> PowHeaderFields {
    assert_eq!(bytes.len(), FIELDS_BYTES);
    let mut fields = [Block128::from(0u128); POW_HEADER_FIELD_COUNT];
    for (i, chunk) in bytes.chunks_exact(16).enumerate() {
        fields[i] = Block128::from(u128::from_le_bytes(chunk.try_into().unwrap()));
    }
    fields
}

fn kat_entry(fields_bytes: &[u8], nonce: u128) -> String {
    let mut fields = decode_pow_fields(fields_bytes);
    // 验证语义：矿工提交的 nonce 覆写 fields[10]（官方 POW_NONCE_FIELD_INDEX）。
    fields[POW_NONCE_FIELD_INDEX] = Block128::from(nonce);
    let digest = poseidon_pow_digest_from_fields(&fields);
    format!(
        "  {{\"fields_hex\": \"{}\", \"nonce_hex\": \"{}\", \"digest_hex\": \"{}\"}}",
        hex::encode(fields_bytes),
        hex::encode(nonce.to_le_bytes()), // 官方 wire 口径：extminer main.rs:189
        hex::encode(digest)
    )
}

fn main() {
    // 固定种子 = "NOID_KAT"（ASCII LE），确定性可复现。
    let mut st = 0x5441_4B5F_4449_4F4Eu64;

    let mut entries: Vec<String> = Vec::new();

    // --- 边界条目 ---
    entries.push(kat_entry(&[0u8; FIELDS_BYTES], 0)); // 全零 fields + nonce=0
    entries.push(kat_entry(&[0xFFu8; FIELDS_BYTES], u128::MAX)); // 全 0xFF + nonce=MAX
    let rf = rand_vec(&mut st, FIELDS_BYTES);
    entries.push(kat_entry(&rf, 0)); // 随机 fields + nonce=0
    let rf2 = rand_vec(&mut st, FIELDS_BYTES);
    entries.push(kat_entry(&rf2, u128::MAX)); // 随机 fields + nonce=MAX

    // --- 随机条目 ---
    for _ in 0..RANDOM_ENTRIES {
        let fb = rand_vec(&mut st, FIELDS_BYTES);
        let nonce_bytes: [u8; 16] = rand_vec(&mut st, 16).try_into().unwrap();
        entries.push(kat_entry(&fb, u128::from_le_bytes(nonce_bytes)));
    }

    println!("[");
    let last = entries.len() - 1;
    for (i, e) in entries.iter().enumerate() {
        if i == last {
            println!("{e}");
        } else {
            println!("{e},");
        }
    }
    println!("]");
}
