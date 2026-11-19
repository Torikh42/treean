INSERT INTO users (id, email, name, password_hash, role)
VALUES
    ('11111111-1111-1111-1111-111111111111', 'organizer@event.local', 'Demo Organizer', '$2a$10$FkmASOgfpqp5X9fpThx5tObnKeOAaEyTP.dM2S84ycPV6Fc.JKnsG', 'ORGANIZER'),
    ('22222222-2222-2222-2222-222222222221', 'buyer1@event.local', 'Demo Buyer 1', '$2a$10$FkmASOgfpqp5X9fpThx5tObnKeOAaEyTP.dM2S84ycPV6Fc.JKnsG', 'BUYER'),
    ('22222222-2222-2222-2222-222222222222', 'buyer2@event.local', 'Demo Buyer 2', '$2a$10$FkmASOgfpqp5X9fpThx5tObnKeOAaEyTP.dM2S84ycPV6Fc.JKnsG', 'BUYER')
ON CONFLICT DO NOTHING;

INSERT INTO events (id, organizer_id, title, description, venue, event_date)
SELECT
    'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa1',
    u.id,
    'Tech Flash Concert 2026',
    'Seeded 100-quota flash sale event with Reguler and VIP tiers',
    'Jakarta Convention Hall',
    '2026-12-20T19:00:00Z'
FROM users u
WHERE u.email = 'organizer@event.local' AND u.role = 'ORGANIZER'
ON CONFLICT DO NOTHING;

INSERT INTO ticket_types (id, event_id, name, price_idr, total_quota, remaining_quota)
SELECT
    v.id::uuid,
    e.id,
    v.name,
    v.price_idr,
    v.total_quota,
    v.remaining_quota
FROM events e
CROSS JOIN (
    VALUES
        ('bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbb1', 'Reguler', 150000, 80, 80),
        ('bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbb2', 'VIP', 350000, 20, 20)
) AS v(id, name, price_idr, total_quota, remaining_quota)
WHERE e.id = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaa1'
ON CONFLICT DO NOTHING;
