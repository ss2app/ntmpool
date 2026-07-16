/* quark.c — line-for-line replica of Noctari node src/hash.h HashQuark().
 * Uses the SAME sphlib sources vendored from the node (blake/bmw/groestl/jh/
 * keccak/skein.c + sph_*.h). Do NOT reimplement: byte divergence = mined
 * garbage blocks (factory rule 2). Conditional branch bit: node checks
 * (hash[n] & arith_uint512(8)) != 0, i.e. bit 3 of the little-endian LSB,
 * which is byte[0] & 0x08. */
#include "quark.h"
#include "sph_blake.h"
#include "sph_bmw.h"
#include "sph_groestl.h"
#include "sph_jh.h"
#include "sph_keccak.h"
#include "sph_skein.h"
#include <string.h>

void quark_hash(const void *input, size_t len, void *output32)
{
    sph_blake512_context   ctx_blake;
    sph_bmw512_context     ctx_bmw;
    sph_groestl512_context ctx_groestl;
    sph_jh512_context      ctx_jh;
    sph_keccak512_context  ctx_keccak;
    sph_skein512_context   ctx_skein;

    unsigned char hash[9][64];

    sph_blake512_init(&ctx_blake);
    sph_blake512(&ctx_blake, input, len);
    sph_blake512_close(&ctx_blake, hash[0]);

    sph_bmw512_init(&ctx_bmw);
    sph_bmw512(&ctx_bmw, hash[0], 64);
    sph_bmw512_close(&ctx_bmw, hash[1]);

    if (hash[1][0] & 8) {
        sph_groestl512_init(&ctx_groestl);
        sph_groestl512(&ctx_groestl, hash[1], 64);
        sph_groestl512_close(&ctx_groestl, hash[2]);
    } else {
        sph_skein512_init(&ctx_skein);
        sph_skein512(&ctx_skein, hash[1], 64);
        sph_skein512_close(&ctx_skein, hash[2]);
    }

    sph_groestl512_init(&ctx_groestl);
    sph_groestl512(&ctx_groestl, hash[2], 64);
    sph_groestl512_close(&ctx_groestl, hash[3]);

    sph_jh512_init(&ctx_jh);
    sph_jh512(&ctx_jh, hash[3], 64);
    sph_jh512_close(&ctx_jh, hash[4]);

    if (hash[4][0] & 8) {
        sph_blake512_init(&ctx_blake);
        sph_blake512(&ctx_blake, hash[4], 64);
        sph_blake512_close(&ctx_blake, hash[5]);
    } else {
        sph_bmw512_init(&ctx_bmw);
        sph_bmw512(&ctx_bmw, hash[4], 64);
        sph_bmw512_close(&ctx_bmw, hash[5]);
    }

    sph_keccak512_init(&ctx_keccak);
    sph_keccak512(&ctx_keccak, hash[5], 64);
    sph_keccak512_close(&ctx_keccak, hash[6]);

    sph_skein512_init(&ctx_skein);
    sph_skein512(&ctx_skein, hash[6], 64);
    sph_skein512_close(&ctx_skein, hash[7]);

    if (hash[7][0] & 8) {
        sph_keccak512_init(&ctx_keccak);
        sph_keccak512(&ctx_keccak, hash[7], 64);
        sph_keccak512_close(&ctx_keccak, hash[8]);
    } else {
        sph_jh512_init(&ctx_jh);
        sph_jh512(&ctx_jh, hash[7], 64);
        sph_jh512_close(&ctx_jh, hash[8]);
    }

    memcpy(output32, hash[8], 32);
}
