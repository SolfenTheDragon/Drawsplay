package com.drawsplay.benchmark;

import java.security.GeneralSecurityException;
import java.security.NoSuchAlgorithmException;
import java.security.SecureRandom;
import java.security.spec.AlgorithmParameterSpec;
import java.util.Locale;
import javax.crypto.Cipher;
import javax.crypto.NoSuchPaddingException;
import javax.crypto.spec.GCMParameterSpec;
import javax.crypto.spec.IvParameterSpec;
import javax.crypto.spec.SecretKeySpec;
import javax.net.ssl.SSLContext;

/** Offline shell-process probe, not product crypto or a TLS performance test. */
public final class CryptoProbe {
    public static final class Result {
        public final boolean supported;
        public final String algorithm;
        public final String provider;
        public final String reason;
        public final int payloadBytes;
        public final long iterations;
        public final long elapsedNanos;

        Result(boolean supported, String algorithm, String provider, String reason,
                int payloadBytes, long iterations, long elapsedNanos) {
            this.supported = supported;
            this.algorithm = algorithm;
            this.provider = provider;
            this.reason = reason;
            this.payloadBytes = payloadBytes;
            this.iterations = iterations;
            this.elapsedNanos = elapsedNanos;
        }

        public double nanosecondsPerOperation() {
            if (!supported || iterations == 0) {
                throw new IllegalStateException("no successful operations");
            }
            return (double) elapsedNanos / iterations;
        }

        public String json() {
            if (!supported) {
                return "{\"type\":\"cryptoProbe\",\"supported\":false,\"algorithm\":"
                        + quote(algorithm) + ",\"reason\":" + quote(reason) + "}";
            }
            return String.format(Locale.ROOT,
                    "{\"type\":\"cryptoProbe\",\"supported\":true,\"algorithm\":%s,"
                    + "\"provider\":%s,\"payloadBytes\":%d,\"iterations\":%d,"
                    + "\"elapsedNanos\":%d,\"nsPerEncryptDecryptPair\":%.3f,"
                    + "\"payloadRoundtripsMbitPerSecond\":%.3f}",
                    quote(algorithm), quote(provider), payloadBytes, iterations, elapsedNanos,
                    nanosecondsPerOperation(), payloadBytes * 8000.0 / nanosecondsPerOperation());
        }
    }

    private static AlgorithmParameterSpec parameters(Cipher cipher, byte[] nonce) {
        if (cipher.getAlgorithm().startsWith("AES/")) {
            return new GCMParameterSpec(128, nonce);
        }
        return new IvParameterSpec(nonce);
    }

    static int encrypt(Cipher cipher, SecretKeySpec key, byte[] nonce,
            byte[] plaintext, byte[] output) throws GeneralSecurityException {
        cipher.init(Cipher.ENCRYPT_MODE, key, parameters(cipher, nonce));
        return cipher.doFinal(plaintext, 0, plaintext.length, output, 0);
    }

    static int decrypt(Cipher cipher, SecretKeySpec key, byte[] nonce,
            byte[] encrypted, byte[] output) throws GeneralSecurityException {
        cipher.init(Cipher.DECRYPT_MODE, key, parameters(cipher, nonce));
        return cipher.doFinal(encrypted, 0, encrypted.length, output, 0);
    }

    static void setCounter(byte[] nonce, long value) {
        if (nonce.length != 12 || value < 0) {
            throw new IllegalArgumentException("require 12-byte nonce and nonnegative counter");
        }
        for (int i = 0; i < 8; i++) {
            nonce[11 - i] = (byte) (value >>> (i * 8));
        }
    }

    static Result measure(String algorithm, int payloadBytes, long milliseconds)
            throws GeneralSecurityException {
        if (payloadBytes < 1 || payloadBytes > 65536 || milliseconds < 1 || milliseconds > 5000) {
            throw new IllegalArgumentException("payload 1..65536 bytes; duration 1..5000 ms");
        }
        final Cipher encryption;
        final Cipher decryption;
        try {
            encryption = Cipher.getInstance(algorithm);
            decryption = Cipher.getInstance(algorithm);
        } catch (NoSuchAlgorithmException | NoSuchPaddingException unavailable) {
            return new Result(false, algorithm, "", unavailable.getClass().getSimpleName(),
                    payloadBytes, 0, 0);
        }
        boolean aes = algorithm.startsWith("AES/");
        byte[] keyBytes = new byte[aes ? 16 : 32];
        new SecureRandom().nextBytes(keyBytes);
        SecretKeySpec key = new SecretKeySpec(keyBytes, aes ? "AES" : "ChaCha20");
        byte[] plaintext = new byte[payloadBytes];
        byte[] encrypted = new byte[payloadBytes + 16];
        byte[] recovered = new byte[payloadBytes];
        byte[] nonce = new byte[12];
        long sequence = 0;
        long iterations = 0;
        long started = 0;
        long elapsed = 0;
        // Reuse payload buffers; provider initialization/parameter objects may allocate.
        while (iterations < 64 || elapsed < milliseconds * 1_000_000L) {
            setCounter(nonce, ++sequence);
            int count = encrypt(encryption, key, nonce, plaintext, encrypted);
            int recoveredCount = decrypt(decryption, key, nonce, encrypted, recovered);
            if (count != encrypted.length || recoveredCount != plaintext.length) {
                throw new GeneralSecurityException("unexpected AEAD output length");
            }
            for (int i = 0; i < recovered.length; i++) {
                if (recovered[i] != plaintext[i]) {
                    throw new GeneralSecurityException("AEAD plaintext verification failed");
                }
            }
            if (iterations == 63) {
                started = System.nanoTime();
            }
            iterations++;
            if (iterations >= 64) {
                elapsed = System.nanoTime() - started;
            }
        }
        return new Result(true, algorithm, encryption.getProvider().getName(), "",
                payloadBytes, iterations - 64, elapsed);
    }

    private static String quote(String text) {
        return "\"" + text.replace("\\", "\\\\").replace("\"", "\\\"") + "\"";
    }

    public static void main(String[] args) throws Exception {
        long milliseconds = args.length == 0 ? 1000 : Long.parseLong(args[0]);
        String[] protocols = SSLContext.getDefault().getSupportedSSLParameters().getProtocols();
        StringBuilder names = new StringBuilder();
        for (String protocol : protocols) {
            if (names.length() > 0) {
                names.append(',');
            }
            names.append(quote(protocol));
        }
        System.out.println("{\"type\":\"tlsCapabilities\",\"supportedProtocols\":["
                + names + "],\"handshakeMeasured\":false}");
        for (String algorithm : new String[] {
                "AES/GCM/NoPadding", "ChaCha20/Poly1305/NoPadding"}) {
            for (int bytes : new int[] {1024, 16384, 65536}) {
                System.out.println(measure(algorithm, bytes, milliseconds).json());
            }
        }
    }
}
