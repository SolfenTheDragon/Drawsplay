//! Encrypted, mutually authenticated transport (Noise XX over any byte stream).
//!
//! Both sides have a long-term X25519 key. The handshake exchanges them; callers
//! decide whether to trust the peer's key (see [`crate::identity::TrustStore`]). For a
//! first-time peer, both users compare the 10-digit [`Session::sas`] code, which is
//! derived from the whole handshake: a man in the middle ends up with two different
//! handshakes and therefore two different codes.
//!
//! After the handshake, [`Session::reader`] / [`Session::writer`] give `Read` / `Write`
//! adapters, so the plain message framing in the crate root works unchanged on top.
//! Records are `u16` big-endian ciphertext length + ChaCha20-Poly1305 ciphertext, with an
//! implicit per-direction counter nonce (TCP delivers in order, so the counters stay in step).

use snow::{Builder, StatelessTransportState};
use std::io::{self, Read, Write};
use std::sync::Arc;

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

fn other<E: std::fmt::Display>(e: E) -> io::Error {
    io::Error::other(e.to_string())
}

pub fn generate_keypair() -> io::Result<Keypair> {
    let b = Builder::new(PARAMS.parse().map_err(other)?);
    let kp = b.generate_keypair().map_err(other)?;
    Ok(Keypair {
        private: kp.private.try_into().map_err(|_| other("bad private key length"))?,
        public: kp.public.try_into().map_err(|_| other("bad public key length"))?,
    })
}

/// A completed handshake.
pub struct Session {
    state: Arc<StatelessTransportState>,
    pub remote_public: PublicKey,
    /// Ten decimal digits (two groups of five) both users compare when pairing a new peer.
    pub sas: String,
}

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
pub fn handshake<S: Read + Write>(s: &mut S, local: &Keypair, initiator: bool) -> io::Result<Session> {
    let b = Builder::new(PARAMS.parse().map_err(other)?).local_private_key(&local.private).map_err(other)?;
    let mut hs = if initiator { b.build_initiator() } else { b.build_responder() }.map_err(other)?;
    let mut buf = vec![0u8; 1024];
    let mut my_turn = initiator;
    while !hs.is_handshake_finished() {
        if my_turn {
            let n = hs.write_message(&[], &mut buf).map_err(other)?;
            write_hs(s, &buf[..n])?;
        } else {
            let m = read_hs(s)?;
            hs.read_message(&m, &mut buf).map_err(other)?;
        }
        my_turn = !my_turn;
    }
    let remote_public: PublicKey = hs
        .get_remote_static()
        .ok_or_else(|| other("peer sent no static key"))?
        .try_into()
        .map_err(|_| other("bad peer key length"))?;
    let h = hs.get_handshake_hash();
    // Ten digits (about 33 bits) in two groups: too many for a man in the middle to grind through
    // ephemeral keys inside the handshake timeout, still easy to read out and compare.
    let n = u64::from_be_bytes([h[0], h[1], h[2], h[3], h[4], h[5], h[6], h[7]]) % 10_000_000_000;
    let sas = format!("{:05} {:05}", n / 100_000, n % 100_000);
    let state = Arc::new(hs.into_stateless_transport_mode().map_err(other)?);
    Ok(Session { state, remote_public, sas })
}

impl Session {
    pub fn reader<R: Read>(&self, inner: R) -> SecureReader<R> {
        SecureReader { inner, state: self.state.clone(), nonce: 0, buf: Vec::new(), pos: 0 }
    }
    pub fn writer<W: Write>(&self, inner: W) -> SecureWriter<W> {
        SecureWriter { inner, state: self.state.clone(), nonce: 0 }
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
}
