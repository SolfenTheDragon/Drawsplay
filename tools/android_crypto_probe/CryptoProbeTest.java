package com.drawsplay.benchmark;

import java.util.Arrays;
import javax.crypto.AEADBadTagException;
import javax.crypto.Cipher;
import javax.crypto.spec.SecretKeySpec;

public final class CryptoProbeTest {
    public static void main(String[] args) throws Exception {
        Cipher encryptor = Cipher.getInstance("AES/GCM/NoPadding");
        SecretKeySpec key = new SecretKeySpec(new byte[16], "AES");
        byte[] plaintext = new byte[16];
        byte[] encrypted = new byte[32];
        int length = CryptoProbe.encrypt(encryptor, key, new byte[12], plaintext, encrypted);
        byte[] expected = hex(
            "0388dace60b6a392f328c2b971b2fe78ab6e47d42cec13bdf53a67b21257bddf");
        require(length == 32 && Arrays.equals(encrypted, expected),
                "AES-GCM known-answer vector differs");

        byte[] recovered = new byte[16];
        int recoveredLength = CryptoProbe.decrypt(
                Cipher.getInstance("AES/GCM/NoPadding"), key, new byte[12], encrypted, recovered);
        require(recoveredLength == 16 && Arrays.equals(recovered, plaintext),
                "AES-GCM decryption differs");
        encrypted[0] ^= 1;
        try {
            CryptoProbe.decrypt(Cipher.getInstance("AES/GCM/NoPadding"),
                    key, new byte[12], encrypted, recovered);
            throw new AssertionError("tampered ciphertext accepted");
        } catch (AEADBadTagException expectedFailure) {
            // Authentication failure is the required result.
        }

        byte[] first = new byte[12];
        byte[] second = new byte[12];
        CryptoProbe.setCounter(first, 1);
        CryptoProbe.setCounter(second, 2);
        require(first[11] == 1 && second[11] == 2 && !Arrays.equals(first, second),
                "nonce counter does not change nonce");
        try {
            CryptoProbe.setCounter(first, -1);
            throw new AssertionError("negative counter accepted");
        } catch (IllegalArgumentException expectedFailure) {
            // Negative counters must not silently wrap.
        }
        CryptoProbe.Result result = CryptoProbe.measure("AES/GCM/NoPadding", 1024, 10);
        require(result.supported && result.iterations > 0 && result.elapsedNanos > 0,
                "AES-GCM measurement missing");
        require(result.payloadBytes == 1024 && result.nanosecondsPerOperation() > 0,
                "measurement units invalid");
        CryptoProbe.Result unavailable = CryptoProbe.measure("not-a-cipher", 1024, 10);
        require(!unavailable.supported && unavailable.reason.length() > 0,
                "unavailable algorithm was hidden");
        try {
            CryptoProbe.measure("AES/GCM/NoPadding", 0, 10);
            throw new AssertionError("zero payload accepted");
        } catch (IllegalArgumentException expectedFailure) {
            // Fixed bounds are required even for this isolated probe.
        }
        System.out.println("CryptoProbeTest: known vector, decrypt, tamper, nonce and result checks passed");
    }

    private static byte[] hex(String text) {
        byte[] bytes = new byte[text.length() / 2];
        for (int i = 0; i < bytes.length; i++) {
            bytes[i] = (byte) Integer.parseInt(text.substring(i * 2, i * 2 + 2), 16);
        }
        return bytes;
    }

    private static void require(boolean value, String message) {
        if (!value) {
            throw new AssertionError(message);
        }
    }
}
