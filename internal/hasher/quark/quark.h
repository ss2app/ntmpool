#ifndef NTMPOOL_QUARK_H
#define NTMPOOL_QUARK_H
#include <stddef.h>
#ifdef __cplusplus
extern "C" {
#endif
/* quark_hash: Noctari (NCTI) Quark PoW. input = block header bytes,
 * output32 = 32-byte little-endian digest (== node CBlockHeader::GetHash()
 * / HashQuark, byte-for-byte). */
void quark_hash(const void *input, size_t len, void *output32);
#ifdef __cplusplus
}
#endif
#endif
