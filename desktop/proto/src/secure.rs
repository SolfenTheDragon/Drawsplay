//! Encrypted, mutually authenticated transport (Noise XX over any byte stream).
//!
//! Both sides have a long-term X25519 key. The handshake exchanges them; callers
//! decide whether to trust the peer's key (see [`crate::identity::TrustStore`]). For a
//! first-time peer, both users compare the 10-digit [`Session::sas`] code, which is
//! derived from the whole handshake: a man in the middle ends up with two different
//! handshakes and therefore two different codes.
//!
//! The code also depends on a random nonce from each side. Each side commits to its nonce (a hash)
//! inside the handshake and reveals it only once the handshake is complete, so an attacker has to
//! fix its own keys before it can know the code, and cannot retry keys until the two codes agree.
//!
//! After the handshake, [`Session::reader`] / [`Session::writer`] give `Read` / `Write`
//! adapters, so the plain message framing in the crate root works unchanged on top.
//! Records are `u16` big-endian ciphertext length + ChaCha20-Poly1305 ciphertext, with an
//! implicit per-direction counter nonce (TCP delivers in order, so the counters stay in step).

use blake2::{Blake2s256, Digest};
use snow::{Builder, StatelessTransportState};
use std::io::{self, Read, Write};
use std::sync::Arc;
use zeroize::Zeroize;

const PARAMS: &str = "Noise_XX_25519_ChaChaPoly_BLAKE2s";
/// Plaintext bytes per record (Noise limits a record to 65535 including the 16-byte tag).
const CHUNK: usize = 60_000;
const TAG: usize = 16;

pub type PublicKey = [u8; 32];

#[derive(Clone)]
pub struct Keypair {
    pub private: [u8; 32],
    pub public: PublicKey,
}

impl Drop for Keypair {
    fn drop(&mut self) {
        self.private.zeroize();
    }
}

/// X25519 inputs of small order: the Diffie-Hellman result with any such key is a known constant, so
/// a peer presenting one proves nothing. (The Noise library does not reject them.) The top bit of the
/// last byte is ignored by X25519, so it is cleared before comparing.
fn is_low_order(key: &PublicKey) -> bool {
    const BAD: [[u8; 32]; 7] = [
        [0; 32],
        {
            let mut k = [0u8; 32];
            k[0] = 1;
            k
        },
        [
            0xe0, 0xeb, 0x7a, 0x7c, 0x3b, 0x41, 0xb8, 0xae, 0x16, 0x56, 0xe3, 0xfa, 0xf1, 0x9f, 0xc4, 0x6a, 0xda, 0x09,
            0x8d, 0xeb, 0x9c, 0x32, 0xb1, 0xfd, 0x86, 0x62, 0x05, 0x16, 0x5f, 0x49, 0xb8, 0x00,
        ],
        [
            0x5f, 0x9c, 0x95, 0xbc, 0xa3, 0x50, 0x8c, 0x24, 0xb1, 0xd0, 0xb1, 0x55, 0x9c, 0x83, 0xef, 0x5b, 0x04, 0x44,
            0x5c, 0xc4, 0x58, 0x1c, 0x8e, 0x86, 0xd8, 0x22, 0x4e, 0xdd, 0xd0, 0x9f, 0x11, 0x57,
        ],
        {
            let mut k = [0xffu8; 32];
            k[0] = 0xec;
            k[31] = 0x7f;
            k
        },
        {
            let mut k = [0xffu8; 32];
            k[0] = 0xed;
            k[31] = 0x7f;
            k
        },
        {
            let mut k = [0xffu8; 32];
            k[0] = 0xee;
            k[31] = 0x7f;
            k
        },
    ];
    let mut k = *key;
    k[31] &= 0x7f;
    BAD.contains(&k)
}

fn other<E: std::fmt::Display>(e: E) -> io::Error {
    io::Error::other(e.to_string())
}

pub fn generate_keypair() -> io::Result<Keypair> {
    let b = Builder::new(PARAMS.parse().map_err(other)?);
    let mut kp = b.generate_keypair().map_err(other)?;
    let pair = (|| -> io::Result<Keypair> {
        Ok(Keypair {
            private: kp.private.as_slice().try_into().map_err(|_| other("bad private key length"))?,
            public: kp.public.as_slice().try_into().map_err(|_| other("bad public key length"))?,
        })
    })();
    kp.private.zeroize();
    pair
}

const NONCE_LEN: usize = 16;
const COMMIT_LEN: usize = 32;

/// Hash that commits `role` ("client" or "host") to `nonce`.
fn commitment(role: &str, nonce: &[u8]) -> [u8; COMMIT_LEN] {
    let mut h = Blake2s256::new();
    h.update(b"drawsplay-sas-commit-v2");
    h.update((role.len() as u8).to_be_bytes());
    h.update(role.as_bytes());
    h.update(nonce);
    h.finalize().into()
}

/// The pairing code: ten digits from the handshake hash and both revealed nonces.
fn sas_code(handshake_hash: &[u8], client_nonce: &[u8], host_nonce: &[u8]) -> String {
    let mut h = Blake2s256::new();
    h.update(b"drawsplay-sas-v2");
    h.update((handshake_hash.len() as u16).to_be_bytes());
    h.update(handshake_hash);
    h.update(client_nonce);
    h.update(host_nonce);
    let d = h.finalize();
    let n = u64::from_be_bytes(d[..8].try_into().unwrap()) % 10_000_000_000;
    format!("{:05} {:05}", n / 100_000, n % 100_000)
}

const OLD_PEER: &str = "the other side uses an older pairing protocol; update DrawSplay Desktop on both computers";

/// A completed handshake.
pub struct Session {
    state: Arc<StatelessTransportState>,
    pub remote_public: PublicKey,
    /// Ten decimal digits (two groups of five) both users compare when pairing a new peer.
    pub sas: String,
}

/// The first record in each direction (nonce 0) carries the SAS nonce; messages start at 1.
const FIRST_MESSAGE_NONCE: u64 = 1;

fn read_hs<S: Read>(s: &mut S) -> io::Result<Vec<u8>> {
    let mut l = [0u8; 2];
    s.read_exact(&mut l)?;
    let mut b = vec![0u8; u16::from_be_bytes(l) as usize];
    s.read_exact(&mut b)?;
    Ok(b)
}

fn write_hs<S: Write>(s: &mut S, b: &[u8]) -> io::Result<()> {
    s.write_all(&(b.len() as u16).to_be_bytes())?;
    s.write_all(b)?;
    s.flush()
}

/// Run the XX handshake. `initiator` is the connecting side (the client).
/// Set a read timeout on the stream first if the peer is not trusted yet.
///
/// Each side's SAS nonce is committed to in the first two handshake messages (so the handshake
/// hash covers both commitments) and revealed over the finished channel, where it is checked.
pub fn handshake<S: Read + Write>(s: &mut S, local: &Keypair, initiator: bool) -> io::Result<Session> {
    let b = Builder::new(PARAMS.parse().map_err(other)?).local_private_key(&local.private).map_err(other)?;
    let mut hs = if initiator { b.build_initiator() } else { b.build_responder() }.map_err(other)?;
    let (my_role, peer_role) = if initiator { ("client", "host") } else { ("host", "client") };
    let mut my_nonce = [0u8; NONCE_LEN];
    getrandom::fill(&mut my_nonce).map_err(other)?;
    let my_commit = commitment(my_role, &my_nonce);
    let mut peer_commit: Option<[u8; COMMIT_LEN]> = None;

    let mut buf = vec![0u8; 1024];
    let mut my_turn = initiator;
    // Messages 0 (initiator) and 1 (responder) carry the commitment as their payload.
    let mut index = 0;
    while !hs.is_handshake_finished() {
        if my_turn {
            let payload: &[u8] = if index < 2 { &my_commit } else { &[] };
            let n = hs.write_message(payload, &mut buf).map_err(other)?;
            write_hs(s, &buf[..n])?;
        } else {
            let m = read_hs(s)?;
            let n = hs.read_message(&m, &mut buf).map_err(other)?;
            if index < 2 {
                peer_commit = Some(buf[..n].try_into().map_err(|_| other(OLD_PEER))?);
            }
        }
        my_turn = !my_turn;
        index += 1;
    }
    let peer_commit = peer_commit.ok_or_else(|| other(OLD_PEER))?;
    let remote_public: PublicKey = hs
        .get_remote_static()
        .ok_or_else(|| other("peer sent no static key"))?
        .try_into()
        .map_err(|_| other("bad peer key length"))?;
    if is_low_order(&remote_public) {
        return Err(other("the other side presented an unusable key"));
    }
    let handshake_hash = hs.get_handshake_hash().to_vec();
    let state = Arc::new(hs.into_stateless_transport_mode().map_err(other)?);

    // Reveal our nonce (record 0 in each direction), then check the peer's against its commitment.
    // Both sides write first; the records are tiny, so the socket buffers them.
    let mut ct = [0u8; NONCE_LEN + TAG];
    let len = state.write_message(0, &my_nonce, &mut ct).map_err(other)?;
    s.write_all(&(len as u16).to_be_bytes())?;
    s.write_all(&ct[..len])?;
    s.flush()?;
    let mut l = [0u8; 2];
    s.read_exact(&mut l)?;
    let len = u16::from_be_bytes(l) as usize;
    if !(TAG..=NONCE_LEN + TAG).contains(&len) {
        return Err(other("bad pairing nonce"));
    }
    let mut peer_ct = vec![0u8; len];
    s.read_exact(&mut peer_ct)?;
    let mut peer_nonce = vec![0u8; len];
    let n = state.read_message(0, &peer_ct, &mut peer_nonce).map_err(other)?;
    peer_nonce.truncate(n);
    if peer_nonce.len() != NONCE_LEN || commitment(peer_role, &peer_nonce) != peer_commit {
        return Err(other("the pairing code check failed: the other side's nonce does not match its commitment"));
    }
    let (client_nonce, host_nonce): (&[u8], &[u8]) =
        if initiator { (&my_nonce, &peer_nonce) } else { (&peer_nonce, &my_nonce) };
    // Ten digits (about 33 bits): the nonces are committed before anyone can know the code, so an
    // attacker gets one try per attempt and cannot grind keys; still easy to read out and compare.
    let sas = sas_code(&handshake_hash, client_nonce, host_nonce);
    Ok(Session { state, remote_public, sas })
}

impl Session {
    pub fn reader<R: Read>(&self, inner: R) -> SecureReader<R> {
        SecureReader { inner, state: self.state.clone(), nonce: FIRST_MESSAGE_NONCE, buf: Vec::new(), pos: 0 }
    }
    pub fn writer<W: Write>(&self, inner: W) -> SecureWriter<W> {
        SecureWriter { inner, state: self.state.clone(), nonce: FIRST_MESSAGE_NONCE }
    }
}

pub struct SecureWriter<W> {
    inner: W,
    state: Arc<StatelessTransportState>,
    nonce: u64,
}

impl<W: Write> Write for SecureWriter<W> {
    fn write(&mut self, data: &[u8]) -> io::Result<usize> {
        if data.is_empty() {
            return Ok(0);
        }
        let n = data.len().min(CHUNK);
        let mut ct = vec![0u8; n + TAG];
        let len = self.state.write_message(self.nonce, &data[..n], &mut ct).map_err(other)?;
        self.nonce += 1;
        let mut rec = Vec::with_capacity(2 + len);
        rec.extend((len as u16).to_be_bytes());
        rec.extend(&ct[..len]);
        self.inner.write_all(&rec)?;
        Ok(n)
    }
    fn flush(&mut self) -> io::Result<()> {
        self.inner.flush()
    }
}

pub struct SecureReader<R> {
    inner: R,
    state: Arc<StatelessTransportState>,
    nonce: u64,
    buf: Vec<u8>,
    pos: usize,
}

impl<R> SecureReader<R> {
    /// The underlying stream, for socket options. Set read timeouts through this: on Windows a
    /// timeout set on another clone of the socket does not apply to the one being read.
    pub fn get_ref(&self) -> &R {
        &self.inner
    }
}

impl<R: Read> Read for SecureReader<R> {
    fn read(&mut self, out: &mut [u8]) -> io::Result<usize> {
        if out.is_empty() {
            return Ok(0);
        }
        if self.pos >= self.buf.len() {
            let mut l = [0u8; 2];
            self.inner.read_exact(&mut l)?;
            let len = u16::from_be_bytes(l) as usize;
            if len < TAG {
                return Err(other("record too short"));
            }
            let mut ct = vec![0u8; len];
            self.inner.read_exact(&mut ct)?;
            let mut pt = vec![0u8; len];
            let n = self.state.read_message(self.nonce, &ct, &mut pt).map_err(other)?;
            self.nonce += 1;
            pt.truncate(n);
            self.buf = pt;
            self.pos = 0;
        }
        let n = out.len().min(self.buf.len() - self.pos);
        out[..n].copy_from_slice(&self.buf[self.pos..self.pos + n]);
        self.pos += n;
        Ok(n)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{read_message, write_message, Message};
    use std::net::{TcpListener, TcpStream};
    use std::thread;

    fn pair() -> (TcpStream, TcpStream) {
        let l = TcpListener::bind("127.0.0.1:0").unwrap();
        let c = TcpStream::connect(l.local_addr().unwrap()).unwrap();
        (c, l.accept().unwrap().0)
    }

    #[test]
    fn handshake_agrees_and_carries_messages() {
        let (mut c, mut s) = pair();
        let (ck, sk) = (generate_keypair().unwrap(), generate_keypair().unwrap());
        let (cpub, spub) = (ck.public, sk.public);
        let t = thread::spawn(move || {
            let sess = handshake(&mut s, &sk, false).unwrap();
            let mut r = sess.reader(s.try_clone().unwrap());
            let mut w = sess.writer(s);
            // Echo one big video frame back (spans several records).
            let m = read_message(&mut r).unwrap();
            write_message(&mut w, &m).unwrap();
            (sess.sas.clone(), sess.remote_public)
        });
        let sess = handshake(&mut c, &ck, true).unwrap();
        assert_eq!(sess.remote_public, spub);
        let mut w = sess.writer(c.try_clone().unwrap());
        let mut r = sess.reader(c);
        let big = Message::Video { pts_us: 9, key: true, data: (0..200_000u32).map(|i| i as u8).collect() };
        write_message(&mut w, &big).unwrap();
        assert_eq!(read_message(&mut r).unwrap(), big);
        let (sas, seen) = t.join().unwrap();
        assert_eq!(sas, sess.sas);
        assert_eq!(seen, cpub);
        assert_eq!(sas.len(), 11); // "12345 67890"
    }

    #[test]
    fn tampered_record_is_rejected() {
        let (mut c, mut s) = pair();
        let (ck, sk) = (generate_keypair().unwrap(), generate_keypair().unwrap());
        let t = thread::spawn(move || {
            let sess = handshake(&mut s, &sk, false).unwrap();
            let mut r = sess.reader(s);
            read_message(&mut r)
        });
        let sess = handshake(&mut c, &ck, true).unwrap();
        // Encrypt a message into memory, flip a byte, send the raw bytes.
        let mut raw = Vec::new();
        write_message(&mut sess.writer(&mut raw), &Message::Ping(1)).unwrap();
        let last = raw.len() - 1;
        raw[last] ^= 1;
        c.write_all(&raw).unwrap();
        assert!(t.join().unwrap().is_err());
    }

    /// Run the real host side against a hand-made client. `reveal` is the nonce the client reveals;
    /// the client always commits to `[7; 16]`. `commit` false sends the old protocol's empty payload.
    fn host_against_fake_client(commit: bool, reveal: [u8; NONCE_LEN]) -> io::Result<Session> {
        let (mut c, mut s) = pair();
        let (ck, sk) = (generate_keypair().unwrap(), generate_keypair().unwrap());
        let t = thread::spawn(move || handshake(&mut s, &sk, false));
        let b = Builder::new(PARAMS.parse().unwrap()).local_private_key(&ck.private).unwrap();
        let mut hs = b.build_initiator().unwrap();
        let mut buf = vec![0u8; 1024];
        let payload = if commit { commitment("client", &[7; NONCE_LEN]).to_vec() } else { Vec::new() };
        let n = hs.write_message(&payload, &mut buf).unwrap();
        write_hs(&mut c, &buf[..n]).unwrap();
        if let Ok(m) = read_hs(&mut c) {
            hs.read_message(&m, &mut buf).unwrap();
            let n = hs.write_message(&[], &mut buf).unwrap();
            write_hs(&mut c, &buf[..n]).unwrap();
            let st = hs.into_stateless_transport_mode().unwrap();
            let mut ct = [0u8; NONCE_LEN + TAG];
            let len = st.write_message(0, &reveal, &mut ct).unwrap();
            c.write_all(&(len as u16).to_be_bytes()).unwrap();
            c.write_all(&ct[..len]).unwrap();
        }
        t.join().unwrap()
    }

    #[test]
    fn matching_reveal_is_accepted_and_a_wrong_one_is_not() {
        assert!(host_against_fake_client(true, [7; NONCE_LEN]).is_ok());
        let err = host_against_fake_client(true, [8; NONCE_LEN]).err().expect("wrong nonce must fail");
        assert!(err.to_string().contains("commitment"), "{err}");
    }

    #[test]
    fn a_peer_without_commitments_is_refused_as_outdated() {
        let err = host_against_fake_client(false, [7; NONCE_LEN]).err().expect("old protocol must fail");
        assert!(err.to_string().contains("older pairing protocol"), "{err}");
    }

    #[test]
    fn the_code_changes_between_sessions_with_the_same_keys() {
        let (ck, sk) = (generate_keypair().unwrap(), generate_keypair().unwrap());
        let mut codes = Vec::new();
        for _ in 0..2 {
            let (mut c, mut s) = pair();
            let sk = sk.clone();
            let t = thread::spawn(move || handshake(&mut s, &sk, false).unwrap().sas);
            let mine = handshake(&mut c, &ck, true).unwrap().sas;
            assert_eq!(t.join().unwrap(), mine);
            codes.push(mine);
        }
        assert_ne!(codes[0], codes[1], "the nonces must change the code");
    }

    #[test]
    fn commitments_are_bound_to_the_role() {
        let n = [1u8; NONCE_LEN];
        assert_ne!(commitment("client", &n), commitment("host", &n));
        assert_eq!(commitment("host", &n), commitment("host", &n));
    }

    #[test]
    fn low_order_keys_are_unusable_and_real_ones_are_not() {
        assert!(is_low_order(&[0; 32]));
        let mut one = [0u8; 32];
        one[0] = 1;
        assert!(is_low_order(&one));
        one[31] = 0x80; // the top bit is ignored by X25519
        assert!(is_low_order(&one));
        for _ in 0..20 {
            assert!(!is_low_order(&generate_keypair().unwrap().public));
        }
    }
}
